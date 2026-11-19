package scheduling

import "time"

// DecisionContext is the immutable input to one main decision.
type DecisionContext struct {
	Site             Site
	Executor         Executor
	Task             TaskType
	SourceQueueID    QueueID
	PrimaryQueueID   QueueID
	AssignmentMethod AssignmentMethod
	Overtime         bool
	Now              time.Time
}

func (c DecisionContext) LocalNow() time.Time {
	loc, err := time.LoadLocation(c.Site.Timezone)
	if err != nil {
		return c.Now.UTC()
	}
	return c.Now.In(loc)
}
