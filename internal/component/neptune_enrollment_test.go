package component

import (
	"strings"
	"testing"
)

func TestValidateNeptuneEnrollmentProfileAcceptsSelectedVoltPipelines(t *testing.T) {
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

func TestSelectedNeptuneCapabilitiesPermitSingleChannels(t *testing.T) {
	for _, service := range []string{"volt", "mastermind"} {
		enabled, disabled := true, false
		archive := saturnEnrollment{NamespaceSlug: service, Token: strings.Repeat("a", 43), ArchivePipeline: &enabled}
		if err := validateNeptuneEnrollmentProfile(service, archive); err != nil {
			t.Fatal("archive-only rejected", err)
		}
		empty := archive
		empty.ArchivePipeline = &disabled
		if err := validateNeptuneEnrollmentProfile(service, empty); err == nil {
			t.Fatal("no capabilities accepted")
		}
		mirror := empty
		mirror.MirrorRoot = service
		mirror.MirrorToken = strings.Repeat("b", 43)
		if service == "volt" {
			mirror.MirrorMode = "single-file"
			mirror.MirrorTargetFilename = "personal.volt"
		} else {
			mirror.MirrorMode = "zip-tree"
			mirror.ReaderRoot = "root"
			mirror.ReaderCapability = "neptune.resource-reader.v1"
			mirror.ReaderToken = strings.Repeat("c", 43)
		}
		if err := validateNeptuneEnrollmentProfile(service, mirror); err != nil {
			t.Fatal("mirror-only rejected", err)
		}
		mirror.ArchivePipeline = &enabled
		if err := validateNeptuneEnrollmentProfile(service, mirror); err != nil {
			t.Fatal("combined rejected", err)
		}
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
