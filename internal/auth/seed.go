package auth

// builtinPolicies defines the system policies. These live in code and are
// never written to the DB. Custom (non-system) policies live in the DB.
var builtinPolicies = []Policy{
	{
		ID:         "system:admin-full-access",
		Name:       "Admin Full Access",
		Effect:     EffectAllow,
		Subjects:   `{"roles":["admin"]}`,
		Actions:    `["*"]`,
		Resources:  `["*"]`,
		Conditions: `{}`,
		Priority:   100,
		IsSystem:   true,
		Enabled:    true,
	},

	{
		ID:         "system:user-own-sessions",
		Name:       "User Own Sessions",
		Effect:     EffectAllow,
		Subjects:   `{"roles":["user"]}`,
		Actions:    `["read","write","create","delete"]`,
		Resources:  `["session"]`,
		Conditions: `{"resource.owner_id":{"eq":"subject.id"}}`,
		Priority:   50,
		IsSystem:   true,
		Enabled:    true,
	},
	{
		ID:         "system:user-own-data",
		Name:       "User Own Data",
		Effect:     EffectAllow,
		Subjects:   `{"roles":["user"]}`,
		Actions:    `["read","write"]`,
		Resources:  `["user_data"]`,
		Conditions: `{"resource.owner_id":{"eq":"subject.id"}}`,
		Priority:   50,
		IsSystem:   true,
		Enabled:    true,
	},
	{
		ID:         "system:user-own-skills",
		Name:       "User Own Skills",
		Effect:     EffectAllow,
		Subjects:   `{"roles":["user"]}`,
		Actions:    `["read","write","create","delete"]`,
		Resources:  `["skill"]`,
		Conditions: `{"resource.owner_id":{"eq":"subject.id"}}`,
		Priority:   50,
		IsSystem:   true,
		Enabled:    true,
	},
	{
		ID:         "system:user-own-profile",
		Name:       "User Own Profile",
		Effect:     EffectAllow,
		Subjects:   `{"roles":["user"]}`,
		Actions:    `["read","write"]`,
		Resources:  `["user"]`,
		Conditions: `{"resource.id":{"eq":"subject.id"}}`,
		Priority:   50,
		IsSystem:   true,
		Enabled:    true,
	},

	{
		ID:         "system:user-own-scheduler",
		Name:       "User Own Scheduler Jobs",
		Effect:     EffectAllow,
		Subjects:   `{"roles":["user"]}`,
		Actions:    `["read","write","create","delete"]`,
		Resources:  `["scheduler"]`,
		Conditions: `{"resource.owner_id":{"eq":"subject.id"}}`,
		Priority:   50,
		IsSystem:   true,
		Enabled:    true,
	},
}

// BuiltinPolicies returns the authoritative list of system policies.
// These are merged with custom DB policies at query time by ListEnabledPolicies.
func BuiltinPolicies() []Policy {
	out := make([]Policy, len(builtinPolicies))
	copy(out, builtinPolicies)
	return out
}
