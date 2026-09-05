package agents

import (
	"errors"
	"strings"
	"testing"
)

func TestParseKeyRequiresStableSelectorKey(t *testing.T) {
	for _, value := range []string{"docs", "docs-support", "a", strings.Repeat("a", 64)} {
		parsed, err := ParseKey(value)
		if err != nil || parsed != value {
			t.Fatalf("ParseKey(%q) = %q, %v", value, parsed, err)
		}
	}
	for _, value := range []string{"", "Docs", " docs", "docs_qa", "-docs", "docs-", strings.Repeat("a", 65)} {
		if _, err := ParseKey(value); !errors.Is(err, ErrInvalid) {
			t.Fatalf("ParseKey(%q) error = %v", value, err)
		}
	}
}

func TestNormalizeConfigurationCanonicalizesTextAndPreservesOrder(t *testing.T) {
	first, second := KnowledgeBaseID{1}, KnowledgeBaseID{2}
	value := validConfiguration(first, second)
	value.DisplayName = "\tDocs Agent  \n"
	value.Description = "\r\nAnswers docs.\r\n"
	value.IdentityInstructions = "\nYou are the documentation specialist.\r\n"
	value.BehavioralInstructions = "\tPrefer concise answers.\r\n"
	value.RefusalMarkdown = "\nI cannot answer that.\r\n"
	normalized, err := NormalizeConfiguration(value)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.DisplayName != "Docs Agent" || normalized.Description != "Answers docs." ||
		normalized.IdentityInstructions != "You are the documentation specialist." ||
		normalized.BehavioralInstructions != "Prefer concise answers." ||
		normalized.RefusalMarkdown != "I cannot answer that." {
		t.Fatalf("normalized configuration = %#v", normalized)
	}
	if len(normalized.KnowledgeBaseIDs) != 2 || normalized.KnowledgeBaseIDs[0] != first || normalized.KnowledgeBaseIDs[1] != second {
		t.Fatalf("membership order changed: %#v", normalized.KnowledgeBaseIDs)
	}
	value.KnowledgeBaseIDs[0] = KnowledgeBaseID{9}
	if normalized.KnowledgeBaseIDs[0] != first {
		t.Fatal("normalized membership slice aliases caller input")
	}
	if err = ValidateConfiguration(value); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unnormalized validation error = %v", err)
	}
}

func TestConfigurationRejectsEmptyDuplicateAndIncoherentMembershipPolicy(t *testing.T) {
	first := KnowledgeBaseID{1}
	tests := []struct {
		name   string
		mutate func(*Configuration)
	}{
		{name: "empty memberships", mutate: func(value *Configuration) { value.KnowledgeBaseIDs = nil }},
		{name: "duplicate memberships", mutate: func(value *Configuration) { value.KnowledgeBaseIDs = []KnowledgeBaseID{first, first} }},
		{name: "zero membership", mutate: func(value *Configuration) { value.KnowledgeBaseIDs = []KnowledgeBaseID{{}} }},
		{name: "zero model", mutate: func(value *Configuration) { value.ModelProfileID = ModelProfileID{} }},
		{name: "single pass tools", mutate: func(value *Configuration) { value.AnswerMode, value.MaxToolCalls = SinglePass, 1 }},
		{name: "tool calling without calls", mutate: func(value *Configuration) { value.MaxToolCalls = 0 }},
		{name: "source without tools", mutate: func(value *Configuration) {
			value.AnswerMode, value.MaxToolCalls, value.EvidenceAccess = SinglePass, 0, WikiAndSource
		}},
		{name: "answer ceiling", mutate: func(value *Configuration) { value.MaxAnswerTokens = MaxAnswerTokens + 1 }},
		{name: "bad language", mutate: func(value *Configuration) { value.ResponseLanguage = "English (US)" }},
		{name: "missing refusal", mutate: func(value *Configuration) { value.RefusalMarkdown = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validConfiguration(first)
			test.mutate(&value)
			if _, err := NormalizeConfiguration(value); !errors.Is(err, ErrInvalid) {
				t.Fatalf("NormalizeConfiguration error = %v", err)
			}
		})
	}
}

func TestCommandValidationRequiresOptimisticVersionAndLifecycleTarget(t *testing.T) {
	configuration := validConfiguration(KnowledgeBaseID{1})
	if err := ValidateCreate(CreateCommand{Key: "docs", Configuration: configuration}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateReplacement(ReplaceConfigurationCommand{AgentID: AgentID{1}, ExpectedVersion: 1, Configuration: configuration}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateLifecycle(SetLifecycleCommand{AgentID: AgentID{1}, ExpectedVersion: 1, Lifecycle: Active}); err != nil {
		t.Fatal(err)
	}
	for _, command := range []SetLifecycleCommand{
		{AgentID: AgentID{}, ExpectedVersion: 1, Lifecycle: Active},
		{AgentID: AgentID{1}, ExpectedVersion: 0, Lifecycle: Active},
		{AgentID: AgentID{1}, ExpectedVersion: 1, Lifecycle: Draft},
	} {
		if err := ValidateLifecycle(command); !errors.Is(err, ErrInvalid) {
			t.Fatalf("ValidateLifecycle(%#v) error = %v", command, err)
		}
	}
}

func validConfiguration(knowledgeBaseIDs ...KnowledgeBaseID) Configuration {
	return Configuration{
		DisplayName:            "Docs Agent",
		Description:            "Answers documentation questions.",
		ResponseLanguage:       "en-US",
		IdentityInstructions:   "You are the documentation specialist.",
		ModelProfileID:         ModelProfileID{7},
		ReasoningEffort:        ReasoningNone,
		AnswerMode:             ToolCalling,
		BehavioralInstructions: "Prefer concise answers.",
		EvidenceAccess:         WikiAndSource,
		RefusalMarkdown:        "I cannot answer that from the available evidence.",
		MaxToolCalls:           8,
		MaxAnswerTokens:        2_048,
		KnowledgeBaseIDs:       knowledgeBaseIDs,
	}
}
