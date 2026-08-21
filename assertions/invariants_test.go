package assertions

import "testing"

func TestActiveRequiresDSSActivated(t *testing.T) {
	valid := Snapshot{
		LocalVersion: 2, LocalStatus: "active", PublishedVersion: 2,
		ConfirmedDSSState: "Activated", DSSReferenceExists: true, DSSState: "Activated",
	}
	if result := ActiveRequiresDSSActivated(valid); !result.Passed {
		t.Fatalf("valid snapshot failed: %#v", result)
	}
	invalid := valid
	invalid.ConfirmedDSSState = "Accepted"
	if result := ActiveRequiresDSSActivated(invalid); result.Passed {
		t.Fatalf("invalid snapshot passed: %#v", result)
	}
}

func TestBlockedVersionHasNoDSSReference(t *testing.T) {
	valid := Snapshot{PublicationSyncStatus: "blocked", DesiredVersion: 2, DSSReferenceExists: true, DSSVersion: 1}
	if result := BlockedVersionHasNoDSSReference(valid); !result.Passed {
		t.Fatalf("older DSS version should be safe: %#v", result)
	}
	invalid := valid
	invalid.DSSVersion = 2
	if result := BlockedVersionHasNoDSSReference(invalid); result.Passed {
		t.Fatalf("blocked desired version unexpectedly passed: %#v", result)
	}
}
