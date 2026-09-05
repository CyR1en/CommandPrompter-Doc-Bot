package agents

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	agentKeyPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)
	languagePattern = regexp.MustCompile(`^[A-Za-z]{2,8}(?:-[A-Za-z0-9]{1,8})*$`)
)

func ParseKey(value string) (string, error) {
	if !utf8.ValidString(value) || !agentKeyPattern.MatchString(value) {
		return "", invalid("agent key must be 1 to 64 lowercase letters, digits, or interior hyphens")
	}
	return value, nil
}

func NormalizeConfiguration(value Configuration) (Configuration, error) {
	var err error
	value.DisplayName, err = normalizeText(value.DisplayName, 255, true, "display name")
	if err != nil {
		return Configuration{}, err
	}
	value.Description, err = normalizeText(value.Description, MaxDescriptionRunes, false, "description")
	if err != nil {
		return Configuration{}, err
	}
	value.ResponseLanguage, err = normalizeText(value.ResponseLanguage, 35, true, "response language")
	if err != nil || !languagePattern.MatchString(value.ResponseLanguage) {
		return Configuration{}, invalid("response language must be a normalized language tag")
	}
	value.IdentityInstructions, err = normalizeText(value.IdentityInstructions, MaxIdentityRunes, true, "identity instructions")
	if err != nil {
		return Configuration{}, err
	}
	value.BehavioralInstructions, err = normalizeText(value.BehavioralInstructions, MaxBehavioralRunes, false, "behavioral instructions")
	if err != nil {
		return Configuration{}, err
	}
	value.RefusalMarkdown, err = normalizeText(value.RefusalMarkdown, MaxRefusalMarkdownRunes, true, "refusal Markdown")
	if err != nil {
		return Configuration{}, err
	}
	value.KnowledgeBaseIDs = append([]KnowledgeBaseID(nil), value.KnowledgeBaseIDs...)
	if err := validateNormalizedConfiguration(value); err != nil {
		return Configuration{}, err
	}
	return value, nil
}

func ValidateConfiguration(value Configuration) error {
	normalized, err := NormalizeConfiguration(value)
	if err != nil {
		return err
	}
	if !equalConfiguration(value, normalized) {
		return invalid("agent configuration fields must already be normalized")
	}
	return nil
}

func ValidateCreate(command CreateCommand) error {
	if _, err := ParseKey(command.Key); err != nil {
		return err
	}
	return ValidateConfiguration(command.Configuration)
}

func ValidateReplacement(command ReplaceConfigurationCommand) error {
	if zeroID(ID(command.AgentID)) || command.ExpectedVersion <= 0 {
		return invalid("agent ID and positive expected_version are required")
	}
	return ValidateConfiguration(command.Configuration)
}

func ValidateLifecycle(command SetLifecycleCommand) error {
	if zeroID(ID(command.AgentID)) || command.ExpectedVersion <= 0 {
		return invalid("agent ID and positive expected_version are required")
	}
	if command.Lifecycle != Active && command.Lifecycle != Archived {
		return invalid("agent lifecycle target must be active or archived")
	}
	return nil
}

func validateNormalizedConfiguration(value Configuration) error {
	if zeroID(ID(value.ModelProfileID)) {
		return invalid("model profile is required")
	}
	if !validReasoningEffort(value.ReasoningEffort) {
		return invalid("reasoning effort is invalid")
	}
	if value.AnswerMode != ToolCalling && value.AnswerMode != SinglePass {
		return invalid("answer mode is invalid")
	}
	if value.EvidenceAccess != WikiOnly && value.EvidenceAccess != WikiAndSource {
		return invalid("evidence access is invalid")
	}
	if value.MaxToolCalls < 0 || value.MaxToolCalls > MaxToolCalls {
		return invalid(fmt.Sprintf("max_tool_calls must be between 0 and %d", MaxToolCalls))
	}
	if value.AnswerMode == SinglePass && value.MaxToolCalls != 0 {
		return invalid("single-pass agents cannot allow model tool calls")
	}
	if value.AnswerMode == ToolCalling && value.MaxToolCalls == 0 {
		return invalid("tool-calling agents require a positive model tool-call limit")
	}
	if value.EvidenceAccess == WikiAndSource && value.AnswerMode != ToolCalling {
		return invalid("source evidence access requires tool-calling mode")
	}
	if value.MaxAnswerTokens < 1 || value.MaxAnswerTokens > MaxAnswerTokens {
		return invalid(fmt.Sprintf("max_answer_tokens must be between 1 and %d", MaxAnswerTokens))
	}
	if len(value.KnowledgeBaseIDs) == 0 || len(value.KnowledgeBaseIDs) > MaxKnowledgeBases {
		return invalid(fmt.Sprintf("an agent must contain 1 to %d knowledge bases", MaxKnowledgeBases))
	}
	seen := make(map[KnowledgeBaseID]struct{}, len(value.KnowledgeBaseIDs))
	for _, id := range value.KnowledgeBaseIDs {
		if zeroID(ID(id)) {
			return invalid("knowledge-base IDs must not be zero")
		}
		if _, exists := seen[id]; exists {
			return invalid("knowledge-base memberships must be unique")
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validReasoningEffort(value ReasoningEffort) bool {
	switch value {
	case ReasoningNone, ReasoningMinimal, ReasoningLow, ReasoningMedium, ReasoningHigh, ReasoningMax:
		return true
	default:
		return false
	}
}

func validLifecycle(value Lifecycle) bool {
	return value == Draft || value == Active || value == Archived
}

func transitionAllowed(current, target Lifecycle) bool {
	switch current {
	case Draft:
		return target == Active || target == Archived
	case Active:
		return target == Archived
	case Archived:
		return target == Active
	default:
		return false
	}
}

func normalizeText(value string, maximum int, required bool, field string) (string, error) {
	if !utf8.ValidString(value) {
		return "", invalid(field + " is not valid UTF-8")
	}
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.TrimFunc(value, pythonWhitespace)
	length := utf8.RuneCountInString(value)
	if required && length == 0 || length > maximum {
		return "", invalid(fmt.Sprintf("%s must contain %d to %d characters", field, btoi(required), maximum))
	}
	for _, character := range value {
		if character == '\x00' || character < 32 && character != '\n' && character != '\t' || character == 127 {
			return "", invalid(field + " contains unsupported control characters")
		}
	}
	return value, nil
}

func pythonWhitespace(value rune) bool {
	return unicode.IsSpace(value) || value >= '\x1c' && value <= '\x1f'
}

func equalConfiguration(left, right Configuration) bool {
	if left.DisplayName != right.DisplayName || left.Description != right.Description ||
		left.ResponseLanguage != right.ResponseLanguage || left.IdentityInstructions != right.IdentityInstructions ||
		left.ModelProfileID != right.ModelProfileID || left.ReasoningEffort != right.ReasoningEffort ||
		left.AnswerMode != right.AnswerMode || left.BehavioralInstructions != right.BehavioralInstructions ||
		left.EvidenceAccess != right.EvidenceAccess || left.RefusalMarkdown != right.RefusalMarkdown ||
		left.MaxToolCalls != right.MaxToolCalls || left.MaxAnswerTokens != right.MaxAnswerTokens ||
		len(left.KnowledgeBaseIDs) != len(right.KnowledgeBaseIDs) {
		return false
	}
	for index := range left.KnowledgeBaseIDs {
		if left.KnowledgeBaseIDs[index] != right.KnowledgeBaseIDs[index] {
			return false
		}
	}
	return true
}

func zeroID(id ID) bool { return id == (ID{}) }

func invalid(message string) error { return fmt.Errorf("%w: %s", ErrInvalid, message) }

func btoi(value bool) int {
	if value {
		return 1
	}
	return 0
}

func conflict(message string) error { return fmt.Errorf("%w: %s", ErrConflict, message) }

func notFound(message string) error { return fmt.Errorf("%w: %s", ErrNotFound, message) }

func isNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
