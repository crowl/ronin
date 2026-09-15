package runtime

import (
	"context"
	"errors"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/plugin"
)

func pluginModel(m llm.Model) plugin.Model {
	return plugin.Model{Provider: m.Provider, Name: m.Name}
}

func pluginUsage(u llm.Usage) *plugin.Usage {
	return &plugin.Usage{
		InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
		CacheReadTokens: u.CachedTokens, CacheWriteTokens: u.CacheWriteTokens,
		Cost: u.Cost.Total, CostAvailable: u.Cost.Available,
	}
}

// predictObserved runs a conversation request and reports its lifecycle to
// plugins. The forwarding goroutine owns both output channels and drains the
// provider on cancellation, keeping observation independent of UI consumption.
func predictObserved(ctx context.Context, client llm.ModelClient, req llm.PredictNextRequest) (<-chan llm.PredictionEvent, <-chan error) {
	host := plugin.FromContext(ctx)
	model := pluginModel(client.Model())
	ctx, op := plugin.Begin(ctx)
	host.Publish(ctx, plugin.ModelRequestStarted{Operation: op, Model: model, Purpose: "conversation"})
	source, sourceErr := client.PredictNext(ctx, req)
	events := make(chan llm.PredictionEvent, 32)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		ended := plugin.ModelRequestEnded{Operation: op, Model: model, Purpose: "conversation"}
		finished, first := false, false
		for event := range source {
			switch e := event.(type) {
			case llm.TextDelta, llm.ThinkingDelta:
				if !first {
					host.Publish(ctx, plugin.ModelRequestFirstOutput{Operation: op})
					first = true
				}
			case llm.PredictionFinished:
				finished = true
				e.Usage.Cost = llm.EstimateCost(client.Model(), e.Usage)
				ended.Usage = pluginUsage(e.Usage)
				ended.StopReason = string(e.StopReason)
			}
			select {
			case events <- event:
			case <-ctx.Done():
			}
		}
		err := <-sourceErr
		if err == nil {
			err = ctx.Err()
		}
		ended.Err = err
		if ended.Err == nil && !finished {
			ended.Err = errors.New("prediction ended without completion")
		}
		host.Publish(ctx, ended)
		if err != nil {
			errs <- err
		}
	}()
	return events, errs
}
