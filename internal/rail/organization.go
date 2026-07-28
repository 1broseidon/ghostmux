package rail

// organizationTarget is the state-only identity moved by m/J/K. Groups are
// identified by name; sessions are always persisted member keys. A window
// normalizes to its owning session before this type is constructed.
type organizationTarget struct {
	group bool
	id    string
}

func organizationTargetOf(r railRow) (organizationTarget, bool) {
	if r.isGroup {
		if r.label == "" {
			return organizationTarget{}, false
		}
		return organizationTarget{group: true, id: r.label}, true
	}
	if r.sess == "" || r.reach {
		return organizationTarget{}, false
	}
	return organizationTarget{id: memberKey(r.sess)}, true
}

func targetCursorIdentity(target organizationTarget) cursorIdentity {
	if target.group {
		return cursorIdentity{group: target.id}
	}
	return cursorIdentity{sess: sessOf(target.id)}
}

// moveOrganization applies one organization step to a clone. It has no model,
// Store, backend, or viewport access, so modal previews and immediate J/K use
// exactly the same semantics without preview writes.
func moveOrganization(groups []Group, target organizationTarget, dir int) ([]Group, bool) {
	out := cloneGroups(groups)
	if dir < 0 {
		dir = -1
	} else if dir > 0 {
		dir = 1
	} else {
		return out, false
	}

	if target.group {
		for i, group := range out {
			if group.Name != target.id {
				continue
			}
			j := i + dir
			if j < 0 || j >= len(out) {
				return out, false
			}
			out[i], out[j] = out[j], out[i]
			return out, true
		}
		return out, false
	}

	gi, mi := findMember(out, target.id)
	if gi < 0 {
		// Ungrouped rows follow every group. Moving one upward enters the tail
		// of the last group; moving it downward has no representable effect.
		if dir < 0 && len(out) > 0 {
			last := len(out) - 1
			out[last].Members = append(out[last].Members, target.id)
			return out, true
		}
		return out, false
	}

	members := out[gi].Members
	switch {
	case dir < 0 && mi > 0:
		members[mi], members[mi-1] = members[mi-1], members[mi]
	case dir > 0 && mi < len(members)-1:
		members[mi], members[mi+1] = members[mi+1], members[mi]
	case dir < 0 && gi > 0:
		out = removeMember(out, target.id)
		out[gi-1].Members = append(out[gi-1].Members, target.id)
	case dir > 0 && gi < len(out)-1:
		out = removeMember(out, target.id)
		out[gi+1].Members = append([]string{target.id}, out[gi+1].Members...)
	case dir > 0:
		// The bottom edge exits organization and returns to the ungrouped rail.
		out = removeMember(out, target.id)
	default:
		return out, false
	}
	return out, true
}

func groupsEqual(a, b []Group) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || len(a[i].Members) != len(b[i].Members) {
			return false
		}
		for j := range a[i].Members {
			if a[i].Members[j] != b[i].Members[j] {
				return false
			}
		}
	}
	return true
}

// cursorIdentity is stable across row rebuilds. A window keeps its exact
// window identity; a hidden window falls back to its session, and a session
// hidden by a folded destination falls back to that group header.
type cursorIdentity struct {
	group  string
	sess   string
	window string
}

func cursorIdentityOf(r railRow) cursorIdentity {
	if r.isGroup {
		return cursorIdentity{group: r.label}
	}
	id := cursorIdentity{sess: r.sess}
	if r.isWin {
		id.window = r.window
	}
	return id
}

func cursorIdentityAt(rows []railRow, cursor int) cursorIdentity {
	if cursor < 0 || cursor >= len(rows) {
		return cursorIdentity{}
	}
	return cursorIdentityOf(rows[cursor])
}

// selectionIndex resolves an identity against raw and visible rows. It keeps
// organization actions on their target without opening folds as a side effect.
func selectionIndex(rows []railRow, collapsed map[string]bool, id cursorIdentity) (int, bool) {
	visible := visibleRows(rows, collapsed)
	if id.group != "" {
		for i, row := range visible {
			if row.isGroup && row.label == id.group {
				return i, true
			}
		}
		return 0, false
	}
	if id.sess == "" {
		return 0, false
	}

	if id.window != "" {
		for i, row := range visible {
			if row.isWin && row.sess == id.sess && row.window == id.window {
				return i, true
			}
		}
	}
	for i, row := range visible {
		if !row.isGroup && !row.isWin && row.sess == id.sess {
			return i, true
		}
	}

	// The exact row is hidden by a folded group. Find its raw group, then use
	// the visible header as the honest stand-in rather than changing fold state.
	targetGroup := ""
	for _, row := range rows {
		if !row.isGroup && row.sess == id.sess {
			targetGroup = row.group
			break
		}
	}
	if targetGroup != "" {
		for i, row := range visible {
			if row.isGroup && row.label == targetGroup {
				return i, true
			}
		}
	}
	return 0, false
}

// organizationSnapshot deliberately excludes dirs. Undo restores organization
// while carrying forward the latest automatically observed directory evidence.
type organizationSnapshot struct {
	groups    []Group
	collapsed map[string]bool
	cursor    cursorIdentity
}

func snapshotOrganization(groups []Group, collapsed map[string]bool, cursor cursorIdentity) organizationSnapshot {
	return organizationSnapshot{
		groups: cloneGroups(groups), collapsed: cloneCollapsed(collapsed), cursor: cursor,
	}
}

type organizationUndo struct {
	snapshot organizationSnapshot
	action   string
}

// moveState keeps persisted organization immutable while draft rows are
// previewed. dirty becomes false again when inverse steps return to original.
type moveState struct {
	target   organizationTarget
	original []Group
	draft    []Group
	label    string
	cursor   cursorIdentity
	dirty    bool
}
