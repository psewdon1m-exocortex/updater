package component

import (
	"strings"
	"testing"
)

func TestValidateNeptuneEnrollmentProfileRequiresVoltDualPipeline(t *testing.T) {
	valid := saturnEnrollment{
		NamespaceSlug: "volt", MirrorRoot: "volt", MirrorToken: "mirror-token",
		MirrorMode: "single-file", MirrorTargetFilename: "personal.volt",
	}
	if err := validateNeptuneEnrollmentProfile("volt", valid); err != nil {
		t.Fatalf("valid Volt dual-pipeline enrollment was rejected: %v", err)
	}

	tests := []struct {
		name       string
		enrollment saturnEnrollment
	}{
		{name: "archive only", enrollment: saturnEnrollment{NamespaceSlug: "volt"}},
		{name: "wrong namespace", enrollment: saturnEnrollment{NamespaceSlug: "chronos", MirrorRoot: "volt", MirrorToken: "mirror-token", MirrorMode: "single-file", MirrorTargetFilename: "personal.volt"}},
		{name: "wrong root", enrollment: saturnEnrollment{NamespaceSlug: "volt", MirrorRoot: "mastermind", MirrorToken: "mirror-token", MirrorMode: "zip-tree"}},
		{name: "wrong mode", enrollment: saturnEnrollment{NamespaceSlug: "volt", MirrorRoot: "volt", MirrorToken: "mirror-token", MirrorMode: "zip-tree", MirrorTargetFilename: "personal.volt"}},
		{name: "wrong target", enrollment: saturnEnrollment{NamespaceSlug: "volt", MirrorRoot: "volt", MirrorToken: "mirror-token", MirrorMode: "single-file", MirrorTargetFilename: "vault.bin"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateNeptuneEnrollmentProfile("volt", test.enrollment); err == nil {
				t.Fatal("invalid Volt enrollment was accepted")
			}
		})
	}
}

func TestValidateNeptuneEnrollmentProfileRejectsCrossServiceCode(t *testing.T) {
	err := validateNeptuneEnrollmentProfile("chronos", saturnEnrollment{NamespaceSlug: "kernel"})
	if err == nil || !strings.Contains(err.Error(), "expected \"chronos\"") {
		t.Fatalf("unexpected validation result: %v", err)
	}
}

func TestMastermindRequiresIndependentArchiveMirrorAndReader(t *testing.T) {
	valid := saturnEnrollment{NamespaceSlug: "mastermind", MirrorRoot: "mastermind", MirrorMode: "zip-tree",
		Token: strings.Repeat("a", 43), MirrorToken: strings.Repeat("b", 43), ReaderToken: strings.Repeat("c", 43),
		ReaderRoot: "root", ReaderCapability: "neptune.resource-reader.v1"}
	if err := validateNeptuneEnrollmentProfile("mastermind", valid); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*saturnEnrollment){
		func(v *saturnEnrollment) { v.ReaderToken = "" },
		func(v *saturnEnrollment) { v.ReaderToken = v.MirrorToken },
		func(v *saturnEnrollment) { v.MirrorMode = "single-file" },
		func(v *saturnEnrollment) { v.MirrorRoot = "volt" },
		func(v *saturnEnrollment) { v.ReaderRoot = "root/../private" },
		func(v *saturnEnrollment) { v.ReaderCapability = "unknown" },
	} {
		value := valid
		change(&value)
		if validateNeptuneEnrollmentProfile("mastermind", value) == nil {
			t.Fatal("Incomplete or mis-scoped enrollment accepted")
		}
	}
}
