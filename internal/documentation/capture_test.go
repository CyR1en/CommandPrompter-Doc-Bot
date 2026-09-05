package docgen

import (
	"testing"

	"github.com/cyr1en/ref0/internal/providers"
)

func TestDocumentationNoOpRequiresSamePlannerAndWriterIdentity(t *testing.T) {
	credential := 7
	models := []CapturedModel{
		{Role: providers.DocumentationPlanner, ProfileID: providers.ProfileID{1}, ProfileVersionID: providers.ProfileVersionID{2}, ProfileVersion: 3, EndpointID: providers.EndpointID{4}, EndpointConfigurationVersion: 5, CredentialVersion: &credential, ReasoningEffort: providers.EffortLow, MaxConcurrentTasks: 1},
		{Role: providers.DocumentationWriter, ProfileID: providers.ProfileID{6}, ProfileVersionID: providers.ProfileVersionID{7}, ProfileVersion: 8, EndpointID: providers.EndpointID{9}, EndpointConfigurationVersion: 10, CredentialVersion: &credential, ReasoningEffort: providers.EffortHigh, MaxConcurrentTasks: 2},
	}
	if !sameDocumentationModels(models, models) {
		t.Fatal("identical documentation model identity did not permit a no-op")
	}
	mutations := []func(*CapturedModel){
		func(value *CapturedModel) { value.ProfileID[0]++ },
		func(value *CapturedModel) { value.ProfileVersionID[0]++ },
		func(value *CapturedModel) { value.ProfileVersion++ },
		func(value *CapturedModel) { value.EndpointID[0]++ },
		func(value *CapturedModel) { value.EndpointConfigurationVersion++ },
		func(value *CapturedModel) { next := *value.CredentialVersion + 1; value.CredentialVersion = &next },
		func(value *CapturedModel) { value.ReasoningEffort = providers.EffortMedium },
		func(value *CapturedModel) { value.MaxConcurrentTasks++ },
	}
	for modelIndex := range models {
		for mutationIndex, mutate := range mutations {
			changed := append([]CapturedModel(nil), models...)
			mutate(&changed[modelIndex])
			if sameDocumentationModels(models, changed) {
				t.Fatalf("documentation model %d identity change %d incorrectly permitted a no-op", modelIndex, mutationIndex)
			}
		}
	}
	if sameDocumentationModels(models, models[:1]) {
		t.Fatal("missing writer incorrectly permitted a no-op")
	}
}
