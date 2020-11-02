package auth

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"strings"

	"github.com/pkg/errors"
	"github.com/rancher/wrangler/pkg/randomtoken"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"
	"golang.org/x/crypto/bcrypt"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	bootstrappedRole       = "authz.management.cattle.io/bootstrapped-role"
	bootstrapAdminConfig   = "admincreated"
	cattleNamespace        = "cattle-system"
	defaultAdminLabelKey   = "authz.management.cattle.io/bootstrapping"
	defaultAdminLabelValue = "admin-user"
	defaultAdminLabel      = map[string]string{defaultAdminLabelKey: defaultAdminLabelValue}
)

func ResetAdmin(ctx *cli.Context) error {
	if err := validation(ctx); err != nil {
		return err
	}
	if err := resetAdmin(ctx); err != nil {
		return errors.Wrap(err, "cluster and rancher are not ready. Please try later.")
	}
	return nil
}

func validation(ctx *cli.Context) error {
	if ctx.String("password") != "" && ctx.String("password-file") != "" {
		return errors.New("only one option can be set for password and password-file")
	}
	return nil
}

func resetAdmin(c *cli.Context) error {
	ctx := context.Background()
	token, err := randomtoken.Generate()
	if err != nil {
		return err
	}
	mustChangePassword := true
	if c.String("password") != "" {
		token = c.String("password")
		mustChangePassword = false
	}
	if c.String("password-file") != "" {
		passwordFromFile, err := ioutil.ReadFile(c.String("password-file"))
		if err != nil {
			return err
		}
		token = strings.TrimSuffix(string(passwordFromFile), "\n")
		mustChangePassword = false
	}

	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		kubeconfig = "/etc/rancher/rke2/rke2.yaml"
	}

	conf, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return err
	}

	client := dynamic.NewForConfigOrDie(conf)
	userClient := client.Resource(schema.GroupVersionResource{
		Group:    "management.cattle.io",
		Version:  "v3",
		Resource: "users",
	})
	configmapClient := kubernetes.NewForConfigOrDie(conf).CoreV1().ConfigMaps(cattleNamespace)
	nodeClient := kubernetes.NewForConfigOrDie(conf).CoreV1().Nodes()
	grbClient := client.Resource(schema.GroupVersionResource{
		Group:    "management.cattle.io",
		Version:  "v3",
		Resource: "globalrolebindings",
	})
	crbClient := client.Resource(schema.GroupVersionResource{
		Group:    "rbac.authorization.k8s.io",
		Version:  "v1",
		Resource: "clusterrolebindings",
	})
	settingClient := client.Resource(schema.GroupVersionResource{
		Group:    "management.cattle.io",
		Version:  "v3",
		Resource: "settings",
	})
	clustersClient := client.Resource(schema.GroupVersionResource{
		Group:    "management.cattle.io",
		Version:  "v3",
		Resource: "clusters",
	})
	var adminName string
	set := labels.Set(defaultAdminLabel)
	admins, err := userClient.List(ctx, v1.ListOptions{LabelSelector: set.String()})
	if err != nil {
		return err
	}

	if len(admins.Items) > 0 {
		adminName = admins.Items[0].GetName()
	}

	if _, err := configmapClient.Get(ctx, bootstrapAdminConfig, v1.GetOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
	} else {
		// if it is already bootstrapped, reset admin password
		set := labels.Set(map[string]string{defaultAdminLabelKey: defaultAdminLabelValue})
		admins, err := userClient.List(ctx, v1.ListOptions{LabelSelector: set.String()})
		if err != nil {
			return err
		}

		count := len(admins.Items)
		if count != 1 {
			var users []string
			for _, u := range admins.Items {
				users = append(users, u.GetName())
			}
			return errors.Errorf("%v users were found with %v label. They are %v. Can only reset the default admin password when there is exactly one user with this label.",
				count, set, users)
		}

		admin := admins.Items[0]
		hash, err := bcrypt.GenerateFromPassword([]byte(token), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		admin.Object["password"] = string(hash)
		admin.Object["mustChangePassword"] = false
		_, err = userClient.Update(ctx, &admin, v1.UpdateOptions{})
		if err != nil {
			return err
		}
		logrus.Infof("Default admin reset. New username: %v, new Password: %v", admin.Object["username"], token)
		return nil
	}

	users, err := userClient.List(ctx, v1.ListOptions{LabelSelector: set.String()})
	if err != nil {
		panic(err)
	}

	if len(users.Items) == 0 {
		// Config map does not exist and no users, attempt to create the default admin user
		hash, _ := bcrypt.GenerateFromPassword([]byte(token), bcrypt.DefaultCost)
		admin, err := userClient.Create(ctx,
			&unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "management.cattle.io/v3",
					"kind":       "User",
					"metadata": v1.ObjectMeta{
						GenerateName: "user-",
						Labels:       defaultAdminLabel,
					},
					"displayName":        "Default Admin",
					"username":           "admin",
					"password":           string(hash),
					"mustChangePassword": mustChangePassword,
				},
			}, v1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
		adminName = admin.GetName()
		if err := setClusterAnnotation(ctx, clustersClient, adminName); err != nil {
			return err
		}

		bindings, err := grbClient.List(ctx, v1.ListOptions{LabelSelector: set.String()})
		if err != nil {
			return err
		}
		if len(bindings.Items) == 0 {
			_, err = grbClient.Create(ctx,
				&unstructured.Unstructured{
					Object: map[string]interface{}{
						"metadata": v1.ObjectMeta{
							GenerateName: "globalrolebinding-",
							Labels:       defaultAdminLabel,
						},
						"apiVersion":     "management.cattle.io/v3",
						"kind":           "GlobalRoleBinding",
						"userName":       adminName,
						"globalRoleName": "admin",
					},
				}, v1.CreateOptions{})
			if err != nil {
				return err
			}
		}

		users, err := userClient.List(ctx, v1.ListOptions{
			LabelSelector: set.String(),
		})

		crbBindings, err := crbClient.List(ctx, v1.ListOptions{LabelSelector: set.String()})
		if err != nil {
			return err
		}
		if len(crbBindings.Items) == 0 && len(users.Items) > 0 {
			_, err = crbClient.Create(ctx,
				&unstructured.Unstructured{
					Object: map[string]interface{}{
						"metadata": v1.ObjectMeta{
							GenerateName: "default-admin-",
							Labels:       defaultAdminLabel,
						},
						"apiVersion": "rbac.authorization.k8s.io/v1",
						"kind":       "ClusterRoleBinding",
						"ownerReferences": []v1.OwnerReference{
							{
								APIVersion: "management.cattle.io/v3",
								Kind:       "user",
								Name:       users.Items[0].GetName(),
								UID:        users.Items[0].GetUID(),
							},
						},
						"subjects": []rbacv1.Subject{
							{
								Kind:     "User",
								APIGroup: rbacv1.GroupName,
								Name:     users.Items[0].GetName(),
							},
						},
						"roleRef": rbacv1.RoleRef{
							APIGroup: rbacv1.GroupName,
							Kind:     "ClusterRole",
							Name:     "cluster-admin",
						},
					},
				}, v1.CreateOptions{})
			if err != nil {
				return err
			}
		}
	}

	_, err = configmapClient.Create(ctx,
		&corev1.ConfigMap{
			ObjectMeta: v1.ObjectMeta{
				Namespace: cattleNamespace,
				Name:      bootstrapAdminConfig,
			},
		}, v1.CreateOptions{})
	if err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
	}

	serverURL := "https://%v:8443"
	nodes, err := nodeClient.List(ctx, v1.ListOptions{})
	if err != nil {
		return err
	}
	if len(nodes.Items) > 0 {
		addresses := nodes.Items[0].Status.Addresses
		// prefer external IP over internal IP
		for _, address := range addresses {
			if address.Type == corev1.NodeExternalIP {
				serverURL = fmt.Sprintf(serverURL, address.Address)
				break
			}
			if address.Type == corev1.NodeInternalIP {
				serverURL = fmt.Sprintf(serverURL, address.Address)
			}
		}
	}

	serverURLSettings, err := settingClient.Get(ctx, "server-url", v1.GetOptions{})
	if err != nil {
		return err
	}
	value := serverURLSettings.Object["value"].(string)
	defaultValue := serverURLSettings.Object["default"].(string)
	if value != "" {
		serverURL = value
	} else if defaultValue != "" {
		serverURL = defaultValue
	}

	logrus.Infof("Server URL: %v", serverURL)
	logrus.Infof("Default admin and password created. Username: admin, Password: %v", token)
	return nil
}

func setClusterAnnotation(ctx context.Context, clustersClient dynamic.NamespaceableResourceInterface, adminName string) error {
	cluster, err := clustersClient.Get(ctx, "local", v1.GetOptions{})
	if err != nil {
		return errors.Errorf("Local cluster is not ready yet")
	}
	if adminName == "" {
		return errors.Errorf("User is not set yet")
	}
	ann := cluster.GetAnnotations()
	if ann == nil {
		ann = make(map[string]string)
	}
	ann["field.cattle.io/creatorId"] = adminName
	cluster.SetAnnotations(ann)

	// reset CreatorMadeOwner condition so that controller will reconcile and reassign admin to the default user
	setConditionToFalse(cluster.Object, "DefaultProjectCreated")
	setConditionToFalse(cluster.Object, "SystemProjectCreated")
	setConditionToFalse(cluster.Object, "CreatorMadeOwner")

	_, err = clustersClient.Update(ctx, cluster, v1.UpdateOptions{})
	return err
}

func setConditionToFalse(object map[string]interface{}, cond string) {
	status, ok := object["status"].(map[string]interface{})
	if !ok {
		return
	}
	conditions, ok := status["conditions"].([]interface{})
	if !ok {
		return
	}
	for _, condition := range conditions {
		m, ok := condition.(map[string]interface{})
		if !ok {
			continue
		}
		if t, ok := m["type"].(string); ok && t == cond {
			m["status"] = "False"
		}
	}
	return
}
