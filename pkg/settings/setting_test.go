package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFullShellImage(t *testing.T) {
	originalShellImage := ShellImage.Get()
	originalDefaultRegistry := SystemDefaultRegistry.Get()

	ShellImage.Set("image")
	SystemDefaultRegistry.Set("")
	assert.Equal(t, "image", FullShellImage())
	SystemDefaultRegistry.Set("prefix")
	assert.Equal(t, "prefix/image", FullShellImage())

	// Reset the settings for other tests
	ShellImage.Set(originalShellImage)
	SystemDefaultRegistry.Set(originalDefaultRegistry)
}

func TestIsRelease(t *testing.T) {
	inputs := map[string]bool{
		"dev":         false,
		"master-head": false,
		"master":      false,
		"v2.5.2":      true,
		"v2":          true,
		"v2.0":        true,
		"v2.x":        true,
		"v2.5-head":   false,
		"2.5":         false,
		"2.5-head":    false,
	}
	a := assert.New(t)
	for key, value := range inputs {
		if err := ServerVersion.Set(key); err != nil {
			t.Errorf("Encountered error while setting temp version: %v\n", err)
		}
		result := IsRelease()
		a.Equal(value, result, fmt.Sprintf("Expected value [%t] for key [%s]. Got value [%t]", value, key, result))
	}
}

// TestSystemDefaultRegistryDefault tests that the default registry is either
// the value set by the environment variable CATTLE_BASE_REGISTRY or the build
// time value set through InjectDefaults.
func TestSystemDefaultRegistryDefault(t *testing.T) {
	expect := os.Getenv("CATTLE_BASE_REGISTRY")
	if InjectDefaults != "" {
		defaults := map[string]string{}
		if err := json.Unmarshal([]byte(InjectDefaults), &defaults); err != nil {
			t.Errorf("Unable to parse InjectDefaults: %v", err)
		}

		if value, ok := defaults["system-default-registry"]; ok {
			expect = value
		}
	}

	got := SystemDefaultRegistry.Get()
	if got != expect {
		t.Errorf("The System Default Registry of %q is not the expected value %q", got, expect)
	}

}

// TestSystemFeatureChartRefreshSecondsDefault tests that the default refresh time is either
// the default value of 21600 seconds or the build time value set through InjectDefaults.
func TestSystemFeatureChartRefreshSecondsDefault(t *testing.T) {
	expect := "21600"
	if InjectDefaults != "" {
		defaults := map[string]string{}
		if err := json.Unmarshal([]byte(InjectDefaults), &defaults); err != nil {
			t.Fatalf("Unable to parse InjectDefaults: %v", err)
		}

		if value, ok := defaults["system-feature-chart-refresh-seconds"]; ok {
			expect = value
		}
	}

	got := SystemFeatureChartRefreshSeconds.Get()
	if got != expect {
		t.Errorf("The System Feature Chart Refresh Seconds of %q is not the expected value %q", got, expect)
	}

}

func TestGetInt(t *testing.T) {
	fakeIntSettting := NewSetting("int", "1")
	fakeStringSetting := NewSetting("string", "one")

	fakeIntSettting.Set("2")
	assert.Equal(t, 2, fakeIntSettting.GetInt())

	fakeIntSettting.Set("two")
	assert.Equal(t, 1, fakeIntSettting.GetInt())

	fakeStringSetting.Set("2")
	assert.Equal(t, 2, fakeStringSetting.GetInt())

	fakeStringSetting.Set("two")
	assert.Equal(t, 0, fakeStringSetting.GetInt())
}

func TestGetRancherVersion(t *testing.T) {
	inputs := map[string]string{
		"dev-version":    RancherVersionDev,
		"master-version": RancherVersionDev,
		"version-head":   RancherVersionDev,
		"v2.7.X":         "2.7.X",
		"2.7.X":          "2.7.X",
	}

	for key, value := range inputs {
		ServerVersion.Set(key)
		result := GetRancherVersion()
		assert.Equal(t, value, result)
	}
}
