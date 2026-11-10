package jobs

import (
	"context"

	"fmt"
	"log/slog"
	"time"

	"github.com/Watthachai/fitt-coderunner/internal/buildstep"
	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

// SetPushHelper has a listed project's build push by itself, through CRN's push helper
// (cmd/crn-push), instead of waiting for an operator's token. helper is the command that
// runs it, such as sudo -n -u _crnpush /opt/crn-push/crn-push; empty keeps the push window.
func (m *manager) SetPushHelper(helper []string) { m.pushHelper = append([]string(nil), helper...) }

// pushAutomatically is the push of a listed project's build when CRN has a push helper. It
// makes the same checks as the push window: every other build has finished and none may
// start, what the agent left is stopped, and the machine is as it was before the agent ran.
// Then the helper, which alone can read the registry login and refuses while any agent
// runs, adds this build's tag; it never writes a tag that exists. Afterwards every tag of the
// GitLab project is read again, and the only change may be this one tag at the helper's
// digest. The record of the build says what was there before and after.
func (m *manager) pushAutomatically(ctx context.Context, job *domain.Job, gate *jobGate, appTag string, log *slog.Logger) (string, error) {
	digest, pushed, err := m.pushThroughHelper(ctx, job, gate, appTag, log)
	if err != nil {
		if !pushed {
			buildstep.RemoveLocalImage(context.WithoutCancel(ctx), appTag, log)
		}
		return "", fmt.Errorf("candidate acceptance: %w", err)
	}
	return digest, nil
}

func (m *manager) pushThroughHelper(ctx context.Context, job *domain.Job, gate *jobGate, appTag string, log *slog.Logger) (digest string, pushed bool, err error) {
	ref, err := buildstep.ParseImageRef(appTag)
	if err != nil {
		return "", false, err
	}
	if err := m.holdBuilds(ctx, job, gate); err != nil {
		return "", false, err
	}
	if _, err := m.stopStrays(ctx, gate.agentStarted, log); err != nil {
		return "", false, err
	}
	if err := m.compareMachine(ctx, gate); err != nil {
		return "", false, err
	}
	before, err := m.readRegistryTags(ctx, ref)
	if err != nil {
		return "", false, err
	}
	if _, exists := before[ref.Repository][ref.Tag]; exists {
		return "", false, fmt.Errorf("%s:%s is already in the registry, and a tag is never pushed twice", ref.Repository, ref.Tag)
	}
	m.publishPhase(job.ID, "push")
	m.publishText(job.ID, fmt.Sprintf("Pushing %s through CRN's push helper. %s holds %d tags before.", appTag, ref.Project, countTags(before)))
	digest, err = buildstep.PushThroughHelper(ctx, m.pushHelper, appTag, log)
	if err != nil {
		m.publish(job.ID, domain.BuildEventMsg{Kind: domain.WSError, Text: "docker push (app) failed: " + err.Error(), JobID: job.ID.String(), Timestamp: time.Now().UTC()})
		return "", false, fmt.Errorf("docker push (app) failed: %w", err)
	}
	after, err := m.readRegistryTags(ctx, ref)
	if err != nil {
		return "", true, err
	}
	if err := buildstep.CompareRegistryTags(before, after, ref.Repository, ref.Tag, digest); err != nil {
		m.publishText(job.ID, "The registry holds something this build did not push: stop this lane until both sides have talked.")
		return "", true, err
	}
	m.publishText(job.ID, fmt.Sprintf("Pushed %s = %s at %s. %s: %d tags before, %d after; the only new tag is %s:%s, and every other tag is as it was.",
		appTag, digest, time.Now().UTC().Format(time.RFC3339), ref.Project, countTags(before), countTags(after), ref.Repository, ref.Tag))
	m.releaseBuilds(gate)
	return digest, true, nil
}

func countTags(tags buildstep.RegistryTags) int {
	n := 0
	for _, t := range tags {
		n += len(t)
	}
	return n
}
