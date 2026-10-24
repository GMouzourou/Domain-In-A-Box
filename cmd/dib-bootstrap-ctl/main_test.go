package main

import "testing"

func TestBootstrapDependencies(t *testing.T) {
	tests := []struct {
		name         string
		corePending  bool
		storkPending bool
		want         []dependency
	}{
		{
			name:        "first core bootstrap without Stork",
			corePending: true,
			want:        []dependency{{"BIND", "192.0.2.10:5353"}, {"Samba LDAP", "127.0.0.1:389"}, {"Samba KDC", "127.0.0.1:88"}},
		},
		{
			name:         "Stork enabled after core bootstrap",
			storkPending: true,
			want:         []dependency{{"Samba LDAP", "127.0.0.1:389"}, {"Samba KDC", "127.0.0.1:88"}, {"PostgreSQL", "postgres.internal:5432"}},
		},
		{
			name:         "initial core and Stork bootstrap",
			corePending:  true,
			storkPending: true,
			want:         []dependency{{"BIND", "192.0.2.10:5353"}, {"Samba LDAP", "127.0.0.1:389"}, {"Samba KDC", "127.0.0.1:88"}, {"PostgreSQL", "postgres.internal:5432"}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := bootstrapDependencies("192.0.2.10", test.corePending, test.storkPending, "postgres.internal", "5432")
			if len(got) != len(test.want) {
				t.Fatalf("got %d dependencies, want %d: %+v", len(got), len(test.want), got)
			}
			for index, want := range test.want {
				if got[index] != want {
					t.Errorf("dependency %d = %+v, want %+v", index, got[index], want)
				}
			}
		})
	}
}
