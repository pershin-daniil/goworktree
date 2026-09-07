package tui

import "context"

// beginOperation gives every visible asynchronous dashboard operation the same
// cancellation boundary and generation guard. The conflict resolver opener is
// intentionally excluded because it keeps its result screen visible.
func (m *workAppModel) beginOperation(kind, title, message string) (context.Context, uint64) {
	if m.operationCancel != nil {
		m.operationCancel()
	}
	operationCtx, cancel := context.WithCancel(m.ctx)
	m.operationCancel = cancel
	m.operationID++
	m.operationKind = kind
	m.operationTitle = title
	m.operationMessage = message
	m.screen = workOperation
	return operationCtx, m.operationID
}
