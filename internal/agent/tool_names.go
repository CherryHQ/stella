package agent

// StellaSettingsToolName is the model-facing name of Stella's read-only
// settings tool. Keep it here so agent-originated turns can exclude the tool
// without making the agent package import the settings implementation.
const StellaSettingsToolName = "stella_settings"
