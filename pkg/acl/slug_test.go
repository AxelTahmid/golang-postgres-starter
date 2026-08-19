package acl

import "testing"

func TestSlugValidate(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		slug    Slug
		wantErr bool
	}{
		"two segments":            {slug: "user.read"},
		"three segments":          {slug: "subscription.plan.write"},
		"digits and underscores":  {slug: "order_v2.read2"},
		"empty":                   {slug: "", wantErr: true},
		"single segment":          {slug: "admin", wantErr: true},
		"trailing dot":            {slug: "user.", wantErr: true},
		"leading dot":             {slug: ".read", wantErr: true},
		"double dot":              {slug: "user..read", wantErr: true},
		"upper case":              {slug: "User.Read", wantErr: true},
		"hyphen":                  {slug: "user-profile.read", wantErr: true},
		"whitespace":              {slug: "user .read", wantErr: true},
		"declared constant is ok": {slug: UserRead},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := tc.slug.Validate()
			if tc.wantErr && err == nil {
				t.Errorf("Validate(%q) = nil, want an error", tc.slug)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("Validate(%q) = %v, want nil", tc.slug, err)
			}
		})
	}
}

func TestStringsAndSlugsRoundTrip(t *testing.T) {
	t.Parallel()

	original := []Slug{SystemAdmin, UserRead, UserWrite}

	values := Strings(original)
	want := []string{"system.admin", "user.read", "user.write"}
	if len(values) != len(want) {
		t.Fatalf("Strings length = %d, want %d", len(values), len(want))
	}
	for i := range want {
		if values[i] != want[i] {
			t.Errorf("Strings[%d] = %q, want %q", i, values[i], want[i])
		}
	}

	back := SlugsFromStrings(values)
	if len(back) != len(original) {
		t.Fatalf("SlugsFromStrings length = %d, want %d", len(back), len(original))
	}
	for i := range original {
		if back[i] != original[i] {
			t.Errorf("SlugsFromStrings[%d] = %q, want %q", i, back[i], original[i])
		}
	}
}

func TestStringsOnEmptyReturnsEmptyNotNil(t *testing.T) {
	t.Parallel()

	if got := Strings(nil); got == nil || len(got) != 0 {
		t.Errorf("Strings(nil) = %#v, want an empty non-nil slice", got)
	}
}
