package workflow

func requireGovernanceFlowMode(actual, requested string) error {
	// Empty is the historical batch default for in-memory definitions and old clients.
	if actual == "" {
		actual = "batch"
	}
	if actual != requested {
		return problem(409, "GOVERNANCE_FLOW_MODE_MISMATCH", "该版本的处理方式与操作不符：批量版本可立即或定时运行，流式版本请使用持续运行管理")
	}
	return nil
}
