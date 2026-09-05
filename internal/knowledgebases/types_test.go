package knowledgebases

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/security"
)

const oracleKey = "v1:MTExMTExMTExMTExMTExMTExMTExMTExMTExMTExMTE"

func TestNameNormalizationAndLifecycleMatchPython(t *testing.T) {
	name, err := ParseName("  Ｄocs Straße  ")
	if err != nil || name.Display != "Docs Straße" || name.Key != "docs strasse" {
		t.Fatalf("name = %#v, %v", name, err)
	}
	if _, err = ParseName(" "); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("blank name error = %v", err)
	}
	if _, err = ParseName(strings.Repeat("ß", 128)); !errors.Is(err, ErrNormalizedName) {
		t.Fatalf("expanded name error = %v", err)
	}
	valid := [][2]Lifecycle{
		{Active, Archived}, {Active, PendingDelete}, {Archived, Active},
		{Archived, PendingDelete}, {PendingDelete, Active},
		{PendingDelete, Archived}, {PendingDelete, Deleted},
	}
	for _, pair := range valid {
		if got, transitionErr := Transition(pair[0], pair[1]); transitionErr != nil || got != pair[1] {
			t.Fatalf("transition %s -> %s = %s, %v", pair[0], pair[1], got, transitionErr)
		}
	}
	invalid := [][2]Lifecycle{
		{Active, Deleted}, {Archived, Deleted}, {PendingDelete, PendingDelete},
		{Deleted, Active}, {Deleted, Archived}, {Deleted, PendingDelete},
	}
	for _, pair := range invalid {
		if _, transitionErr := Transition(pair[0], pair[1]); !errors.Is(transitionErr, ErrTransition) || !errors.Is(transitionErr, ErrConflict) {
			t.Fatalf("transition %s -> %s error = %v", pair[0], pair[1], transitionErr)
		}
	}
	now := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	if RestoreLifecycle(nil) != Active || RestoreLifecycle(&now) != Archived {
		t.Fatal("restore lifecycle did not preserve deletion origin")
	}
}

func TestCommandValidationAndDigestGoldenValues(t *testing.T) {
	name, err := ParseName("Docs Straße")
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateCreate(CreateCommand{
		Name: name, Access: Restricted, Instructions: "Document public commands.", Language: "en-US",
	}); err != nil {
		t.Fatalf("create validation: %v", err)
	}
	if err = ValidateUpdate(UpdateCommand{ExpectedVersion: 1}); err == nil || err.Error() != "knowledge base update must change a field" {
		t.Fatalf("empty update error = %v", err)
	}
	pending := PendingDelete
	if err = ValidateUpdate(UpdateCommand{ExpectedVersion: 1, Lifecycle: &pending}); err == nil || err.Error() != "updates can only activate or archive" {
		t.Fatalf("pending update error = %v", err)
	}
	vault, err := security.NewCredentialVault(oracleKey, "")
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{vault: vault}
	request, err := service.request(auth.OperatorID{}, "create", "knowledge_base.create",
		[]byte("knowledge_base.create"), []byte("Docs Straße"), []byte("RESTRICTED"),
		[]byte("Document public commands."), []byte("en-US"),
	)
	if err != nil || hex.EncodeToString(request.Digest[:]) != "4fb1ef09296a0383e267dca37a62b31ab1c63ed2d90c45f367d999792ba9c97b" {
		t.Fatalf("create digest = %x, %v", request.Digest, err)
	}
	id := ID{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	newName := Name{Display: "Duplicate", Key: "duplicate"}
	access := Public
	language := "en"
	lifecycle := Archived
	request, err = service.request(auth.OperatorID{}, "update", "knowledge_base.update",
		[]byte("knowledge_base.update"), id[:], versionBytes(1), optionalName(&newName),
		optionalAccess(&access), optionalString(nil), optionalString(&language), optionalLifecycle(&lifecycle),
	)
	if err != nil || hex.EncodeToString(request.Digest[:]) != "cd08f1c775b67ed5409b684b208257ce432199a19434b2b47e868b229890a7b3" {
		t.Fatalf("update digest = %x, %v", request.Digest, err)
	}
}

func TestSnapshotUsesRFC3339AndNullableShapes(t *testing.T) {
	created := time.Date(2026, 8, 30, 12, 34, 56, 123456000, time.UTC)
	value := KnowledgeBase{
		ID: ID{1}, Name: "Docs", Access: Restricted, Lifecycle: Active,
		Instructions: "instructions", Language: "en-US", CreatedAt: created,
		UpdatedAt: created, Version: 1,
	}
	snapshot := snapshot(value)
	if snapshot["created_at"] != "2026-08-30T12:34:56.123456+00:00" ||
		snapshot["access"] != "restricted" || snapshot["lifecycle"] != "active" ||
		snapshot["published_wiki_id"] != nil || snapshot["purge_after"] != nil {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}
