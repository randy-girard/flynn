package data

import (
	"fmt"
	"sort"
	"strings"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/postgres"
)

// Renaming an app (NEW-59). Since CORE-17 a user app may only declare
// discoverd services named "<app>" or "<app>-*", in the controller when a
// release is created and in flynn-host when a job starts. Service names live
// in releases (ProcessType.Service, port services) and in routes, so a
// rename has to keep them consistent:
//
//   - If the current release, or a release that still has processes scaled
//     up, declares a service named after the old name, the rename is
//     refused. Rewriting it would need a new release and a deploy, and the
//     routes would point at no backends until that deploy finished.
//   - Otherwise the rename goes ahead and every live route of the app whose
//     service is named after the old name is moved to the new name in the
//     same transaction. Nothing serves those names yet, so no traffic is
//     lost; the next push (which names its web service after the new name)
//     is routed, and nobody who later takes the old name receives this
//     app's traffic.

// renamedService returns the service name under the new app name, and
// whether service belongs to the app under its old name: "<old>" or
// "<old>-*", unless a more specific existing app owns it ("shop-admin-web"
// belongs to an app "shop-admin", not to "shop").
func renamedService(tx rowQueryer, appID, oldName, newName, service string) (string, bool, error) {
	if service != oldName && !strings.HasPrefix(service, oldName+"-") {
		return "", false, nil
	}
	for i := len(service); i > len(oldName); i-- {
		if i < len(service) && service[i] != '-' {
			continue
		}
		owner, err := selectApp(tx, service[:i], false)
		if err == ErrNotFound {
			continue
		}
		if err != nil {
			return "", false, err
		}
		if owner.ID != appID {
			return "", false, nil
		}
	}
	return newName + service[len(oldName):], true, nil
}

// releaseRenameConflicts lists the services named after oldName that the
// app's current release, or a release with processes still scaled up,
// declares.
func releaseRenameConflicts(tx *postgres.DBTx, app *ct.App, newName string) ([]string, error) {
	rows, err := tx.Query(`
SELECT r.processes FROM releases r
WHERE r.app_id = $1 AND r.deleted_at IS NULL AND (
  r.release_id::text = $2 OR EXISTS (
    SELECT 1 FROM formations f, jsonb_each_text(COALESCE(f.processes, '{}'::jsonb)) p
    WHERE f.app_id = r.app_id AND f.release_id = r.release_id AND f.deleted_at IS NULL
      AND p.value ~ '^[0-9]+$' AND p.value::bigint > 0))`, app.ID, app.ReleaseID)
	if err != nil {
		return nil, err
	}
	var names []string
	for rows.Next() {
		var procs map[string]ct.ProcessType
		if err := rows.Scan(&procs); err != nil {
			rows.Close()
			return nil, err
		}
		for _, proc := range procs {
			names = append(names, proc.Service)
			for _, p := range proc.Ports {
				if p.Service != nil {
					names = append(names, p.Service.Name)
				}
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var out []string
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		_, owned, err := renamedService(tx, app.ID, app.Name, newName, name)
		if err != nil {
			return nil, err
		}
		if owned {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// checkRename refuses a rename that would leave live releases declaring
// services the app no longer owns.
func checkRename(tx *postgres.DBTx, app *ct.App, newName string) error {
	if app.System() {
		return nil
	}
	conflicts, err := releaseRenameConflicts(tx, app, newName)
	if err != nil {
		return err
	}
	if len(conflicts) == 0 {
		return nil
	}
	quoted := make([]string, len(conflicts))
	for i, c := range conflicts {
		quoted[i] = fmt.Sprintf("%q", c)
	}
	return ct.ValidationError{
		Field: "name",
		Message: fmt.Sprintf(
			"cannot change from %q to %q: its release declares the services %s, and an app may only use services named %q or %q, so the next push, deploy or job restart would be refused. Create a new app named %q and deploy there instead",
			app.Name, newName, strings.Join(quoted, ", "), newName, newName+"-*", newName),
	}
}

// moveRenamedRoutes points the app's live routes at the services' new names.
func (r *AppRepo) moveRenamedRoutes(tx *postgres.DBTx, app *ct.App, oldName, newName string) error {
	if app.System() {
		return nil
	}
	parentRef := ct.RouteParentRefPrefix + app.ID
	for _, typ := range []string{"http", "tcp"} {
		rows, err := tx.Query(fmt.Sprintf(`SELECT id, service FROM %s_routes WHERE parent_ref = $1 AND deleted_at IS NULL`, typ), parentRef)
		if err != nil {
			return err
		}
		type move struct{ id, service string }
		var moves []move
		for rows.Next() {
			var m move
			if err := rows.Scan(&m.id, &m.service); err != nil {
				rows.Close()
				return err
			}
			moves = append(moves, m)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, m := range moves {
			next, owned, err := renamedService(tx, app.ID, oldName, newName, m.service)
			if err != nil {
				return err
			}
			if !owned {
				continue
			}
			if err := tx.Exec(fmt.Sprintf(`UPDATE %s_routes SET service = $2, updated_at = now() WHERE id = $1`, typ), m.id, next); err != nil {
				return err
			}
			var scan = scanHTTPRoute
			if typ == "tcp" {
				scan = scanTCPRoute
			}
			route, err := scan(tx.QueryRow(typ+"_route_select", m.id))
			if err != nil {
				return err
			}
			if err := r.routes.createEvent(tx, route, ct.EventTypeRoute); err != nil {
				return err
			}
		}
	}
	return nil
}
