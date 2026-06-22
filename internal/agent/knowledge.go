package agent

import pkgplugins "github.com/CherryHQ/stella/pkg/plugins"

func knowledgeStoreFromSkillStore(store pkgplugins.SkillStore) pkgplugins.KnowledgeStore {
	if store == nil {
		return nil
	}
	ks, _ := store.(pkgplugins.KnowledgeStore)
	return ks
}
