// SPDX-License-Identifier: AGPL-3.0-or-later

package controlplane

import (
	"reflect"
	"testing"

	"github.com/thefoxbyte/foxbyte/internal/auth"
	"github.com/thefoxbyte/foxbyte/internal/branch"
)

func TestImportAs(t *testing.T) {
	for _, c := range []struct {
		name  string
		u     auth.User
		admin bool
		want  branch.As
		ok    bool
	}{
		{"an admin imports as the operator", auth.User{Email: "root@example.com"}, true, branch.As{}, true},
		{"anyone else as themselves", auth.User{Email: "ann@example.com"}, false, branch.As{Account: "ann@example.com"}, true},
		{"nobody is refused, not run as the superuser", auth.User{}, false, branch.As{}, false},
	} {
		got, ok := importAsFor(c.u, c.admin)
		if !reflect.DeepEqual(got, c.want) || ok != c.ok {
			t.Errorf("%s: got %+v %v, want %+v %v", c.name, got, ok, c.want, c.ok)
		}
	}
}
