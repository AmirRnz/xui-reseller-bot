package handlers

import "testing"

func TestDetermineTestLimit(t *testing.T) {
	tests := []struct {
		name                 string
		isApproved           bool
		planMaxPerDay        int
		unapprovedSettingStr string
		expectedLimit        int
	}{
		{
			name:                 "Approved user gets plan max per day",
			isApproved:           true,
			planMaxPerDay:        5,
			unapprovedSettingStr: "1",
			expectedLimit:        5,
		},
		{
			name:                 "Unapproved user gets setting limit",
			isApproved:           false,
			planMaxPerDay:        5,
			unapprovedSettingStr: "2",
			expectedLimit:        2,
		},
		{
			name:                 "Unapproved user gets default 1 if setting is empty",
			isApproved:           false,
			planMaxPerDay:        5,
			unapprovedSettingStr: "",
			expectedLimit:        1,
		},
		{
			name:                 "Unapproved user gets default 1 if setting is invalid",
			isApproved:           false,
			planMaxPerDay:        5,
			unapprovedSettingStr: "invalid",
			expectedLimit:        1,
		},
		{
			name:                 "Unapproved user gets default 1 if setting is negative",
			isApproved:           false,
			planMaxPerDay:        5,
			unapprovedSettingStr: "-5",
			expectedLimit:        1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limit := DetermineTestLimit(tt.isApproved, tt.planMaxPerDay, tt.unapprovedSettingStr)
			if limit != tt.expectedLimit {
				t.Errorf("expected %d, got %d", tt.expectedLimit, limit)
			}
		})
	}
}
