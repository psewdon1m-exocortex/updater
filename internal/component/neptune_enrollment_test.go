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
