// SPDX-License-Identifier: AGPL-3.0-or-later

package controlplane

import (
	"errors"
	"net/http"

	"github.com/thefoxbyte/foxbyte/internal/auth"
	"github.com/thefoxbyte/foxbyte/internal/branch"
)

// errNoImportAccount refuses an import that cannot say whose it is.
var errNoImportAccount = errors.New("this request is not tied to an account, so it cannot import — sign in, or use an API key that belongs to one")

// importAs is whose an import, or a pipeline run, started from the API is.
//
// An admin's runs as the operator's always have, as the superuser. Anyone
// else's runs what the source supplies as their own role, so it can do on the
// new branch what they could do there through the gateway and no more
// (branch.As). An account with no email has no role to run as; ok is false and
// the caller refuses, rather than quietly running it as the superuser.
func importAs(r *http.Request) (as branch.As, ok bool) {
	u, _ := auth.UserFrom(r.Context())
	return importAsFor(u, isAdmin(r))
}

func importAsFor(u auth.User, admin bool) (branch.As, bool) {
	if admin {
		return branch.As{}, true
	}
	if u.Email == "" {
		return branch.As{}, false
	}
	return branch.As{Account: u.Email}, true
}
