package backup

import "sort"

// loadPlan is the order to insert tables in (parents first) and, per table,
// the columns that are left NULL on insert and filled in once every table is
// loaded: self references and the columns that break a cycle.
type loadPlan struct {
	Order    []string
	Deferred map[string][]string
}

func planLoad(names []string, fks []foreignKey) loadPlan {
	in := make(map[string]bool, len(names))
	for _, n := range names {
		in[n] = true
	}
	p := loadPlan{Deferred: map[string][]string{}}
	deps := map[string]map[string][]string{} // child -> parent -> columns
	for _, fk := range fks {
		if !in[fk.Table] || !in[fk.RefTable] {
			continue
		}
		if fk.Table == fk.RefTable {
			p.Deferred[fk.Table] = appendUnique(p.Deferred[fk.Table], fk.Column)
			continue
		}
		if deps[fk.Table] == nil {
			deps[fk.Table] = map[string][]string{}
		}
		deps[fk.Table][fk.RefTable] = appendUnique(deps[fk.Table][fk.RefTable], fk.Column)
	}

	done := map[string]bool{}
	for len(p.Order) < len(names) {
		progressed := false
		for _, n := range names {
			if done[n] {
				continue
			}
			ready := true
			for parent := range deps[n] {
				if !done[parent] {
					ready = false
					break
				}
			}
			if ready {
				p.Order = append(p.Order, n)
				done[n] = true
				progressed = true
			}
		}
		if progressed {
			continue
		}
		// A cycle: load the first waiting table now and fill in its links
		// to tables not loaded yet afterwards.
		for _, n := range names {
			if done[n] {
				continue
			}
			for parent, cols := range deps[n] {
				if !done[parent] {
					for _, c := range cols {
						p.Deferred[n] = appendUnique(p.Deferred[n], c)
					}
					delete(deps[n], parent)
				}
			}
			break
		}
	}
	for t := range p.Deferred {
		sort.Strings(p.Deferred[t])
	}
	return p
}

func appendUnique(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}
