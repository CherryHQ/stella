package reflect

import "github.com/CherryHQ/stella/pkg/ai"

const (
	toolSubmitKnowledgeRelatedDiscovery = "submit_knowledge_related_discovery"
	toolSubmitSkillRelatedDiscovery     = "submit_skill_related_discovery"
	toolSubmitFactReconciliation        = "submit_fact_reconciliation"
	toolSubmitSkillReconciliation       = "submit_skill_reconciliation"
)

type knowledgeRelatedDiscoveryCapturePayload struct {
	Selections []knowledgeRelatedSelection `json:"selections"`
}

type skillRelatedDiscoveryCapturePayload struct {
	Selections []skillRelatedSelection `json:"selections"`
}

type factReconciliationCapturePayload struct {
	Plan factReconciliationPlan `json:"plan"`
}

type skillReconciliationCapturePayload struct {
	Plan skillReconciliationPlan `json:"plan"`
}

func decodeKnowledgeRelatedDiscoveryCall(calls []ai.ToolCall) ([]knowledgeRelatedSelection, error) {
	payload, err := decodeSingleCapturePayload[knowledgeRelatedDiscoveryCapturePayload](calls, toolSubmitKnowledgeRelatedDiscovery)
	if err != nil {
		return nil, err
	}
	return payload.Selections, nil
}

func decodeSkillRelatedDiscoveryCall(calls []ai.ToolCall) ([]skillRelatedSelection, error) {
	payload, err := decodeSingleCapturePayload[skillRelatedDiscoveryCapturePayload](calls, toolSubmitSkillRelatedDiscovery)
	if err != nil {
		return nil, err
	}
	return payload.Selections, nil
}

func decodeFactReconciliationCall(calls []ai.ToolCall) (factReconciliationPlan, error) {
	payload, err := decodeSingleCapturePayload[factReconciliationCapturePayload](calls, toolSubmitFactReconciliation)
	if err != nil {
		return factReconciliationPlan{}, err
	}
	return payload.Plan, nil
}

func decodeSkillReconciliationCall(calls []ai.ToolCall) (skillReconciliationPlan, error) {
	payload, err := decodeSingleCapturePayload[skillReconciliationCapturePayload](calls, toolSubmitSkillReconciliation)
	if err != nil {
		return skillReconciliationPlan{}, err
	}
	return payload.Plan, nil
}
