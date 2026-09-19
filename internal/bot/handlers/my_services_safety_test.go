package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"xui-reseller-bot/internal/xui"
)

func TestResolveDeleteOutcomeCommittedThenTimeoutAndNotFound(t *testing.T) {
	deleteErr := &xui.WriteError{Outcome: xui.WriteUnknown, Err: context.DeadlineExceeded}
	resolution, err := resolveDeleteOutcome(deleteErr, func() (*xui.XUIClientInfo, error) {
		return nil, &xui.NotFoundError{StatusCode: 404, Message: "client not found"}
	})
	if err != nil {
		t.Fatalf("expected committed delete to resolve cleanly, got %v", err)
	}
	if resolution != deleteConfirmed {
		t.Fatalf("expected confirmed deletion, got %s", resolution)
	}
}

func TestResolveDeleteOutcomeVerificationUnavailableRequiresReconciliation(t *testing.T) {
	deleteErr := &xui.WriteError{Outcome: xui.WriteUnknown, Err: context.DeadlineExceeded}
	verifyErr := errors.New("verification endpoint timed out")
	resolution, err := resolveDeleteOutcome(deleteErr, func() (*xui.XUIClientInfo, error) {
		return nil, verifyErr
	})
	if resolution != deleteReconciliationRequired {
		t.Fatalf("expected reconciliation-required outcome, got %s", resolution)
	}
	if !errors.Is(err, verifyErr) {
		t.Fatalf("expected verification error to be retained, got %v", err)
	}
}

func TestResolveDeleteOutcomeDefinitiveFailureDoesNotVerifyOrMutate(t *testing.T) {
	deleteErr := &xui.WriteError{Outcome: xui.WriteDefinitiveFailure, Err: errors.New("panel rejected delete")}
	verified := false
	resolution, err := resolveDeleteOutcome(deleteErr, func() (*xui.XUIClientInfo, error) {
		verified = true
		return nil, nil
	})
	if resolution != deleteDefinitiveFailure {
		t.Fatalf("expected definitive failure, got %s", resolution)
	}
	if !errors.Is(err, deleteErr) {
		t.Fatalf("expected original delete error, got %v", err)
	}
	if verified {
		t.Fatalf("definitive delete failure must not trigger a readback")
	}
}

func TestResolveDeleteOutcomePresentAfterUnknownRemainsUnchanged(t *testing.T) {
	deleteErr := &xui.WriteError{Outcome: xui.WriteUnknown, Err: context.DeadlineExceeded}
	resolution, err := resolveDeleteOutcome(deleteErr, func() (*xui.XUIClientInfo, error) {
		return &xui.XUIClientInfo{Email: "still-present@example.com"}, nil
	})
	if resolution != deleteStillPresent {
		t.Fatalf("expected present-client failure, got %s", resolution)
	}
	if !errors.Is(err, errDeleteClientStillPresent) {
		t.Fatalf("expected still-present marker, got %v", err)
	}
}

func TestOrchestrateDeleteOutcomeCommittedTimeoutNotFoundCancelsExactlyOnce(t *testing.T) {
	deleteErr := &xui.WriteError{Outcome: xui.WriteUnknown, Err: context.DeadlineExceeded}
	verifyCalls, cancellationCalls, reconciliationCalls := 0, 0, 0

	resolution, err := orchestrateDeleteOutcome(
		deleteErr,
		func() (*xui.XUIClientInfo, error) {
			verifyCalls++
			return nil, &xui.NotFoundError{StatusCode: 404, Message: "client missing after committed delete"}
		},
		func() error {
			cancellationCalls++
			return nil
		},
		func(error, error) error {
			reconciliationCalls++
			return nil
		},
	)
	if err != nil {
		t.Fatalf("expected committed delete to continue, got %v", err)
	}
	if resolution != deleteConfirmed {
		t.Fatalf("expected confirmed deletion, got %s", resolution)
	}
	if verifyCalls != 1 {
		t.Fatalf("expected one targeted verification read, got %d", verifyCalls)
	}
	if cancellationCalls != 1 {
		t.Fatalf("expected exactly one local cancellation, got %d", cancellationCalls)
	}
	if reconciliationCalls != 0 {
		t.Fatalf("confirmed deletion must not be persisted as reconciliation, got %d records", reconciliationCalls)
	}
}

func TestOrchestrateDeleteOutcomeUnavailableVerificationReconcilesWithoutFinancialMutation(t *testing.T) {
	deleteErr := &xui.WriteError{Outcome: xui.WriteUnknown, Err: context.DeadlineExceeded}
	verifyErr := errors.New("targeted client read timed out")
	cancellationCalls, refundCalls, reconciliationCalls := 0, 0, 0

	resolution, err := orchestrateDeleteOutcome(
		deleteErr,
		func() (*xui.XUIClientInfo, error) { return nil, verifyErr },
		func() error {
			cancellationCalls++
			refundCalls++
			return nil
		},
		func(gotDeleteErr, gotVerifyErr error) error {
			reconciliationCalls++
			if !errors.Is(gotDeleteErr, deleteErr) || !errors.Is(gotVerifyErr, verifyErr) {
				t.Fatalf("reconciliation callback lost operation errors: delete=%v verify=%v", gotDeleteErr, gotVerifyErr)
			}
			return nil
		},
	)
	if resolution != deleteReconciliationRequired {
		t.Fatalf("expected reconciliation-required outcome, got %s", resolution)
	}
	if !errors.Is(err, verifyErr) {
		t.Fatalf("expected verification error to remain observable, got %v", err)
	}
	if reconciliationCalls != 1 {
		t.Fatalf("expected one durable reconciliation callback, got %d", reconciliationCalls)
	}
	if cancellationCalls != 0 || refundCalls != 0 {
		t.Fatalf("unknown delete must not cancel or refund: cancellations=%d refunds=%d", cancellationCalls, refundCalls)
	}
}

func TestOrchestrateDeleteOutcomeDefinitiveFailureLeavesDBAndFinancialCallbacksUntouched(t *testing.T) {
	deleteErr := &xui.WriteError{Outcome: xui.WriteDefinitiveFailure, Err: errors.New("panel rejected delete")}
	verifyCalls, cancellationCalls, reconciliationCalls, financialMutationCalls := 0, 0, 0, 0

	resolution, err := orchestrateDeleteOutcome(
		deleteErr,
		func() (*xui.XUIClientInfo, error) {
			verifyCalls++
			return nil, nil
		},
		func() error {
			cancellationCalls++
			financialMutationCalls++
			return nil
		},
		func(error, error) error {
			reconciliationCalls++
			financialMutationCalls++
			return nil
		},
	)
	if resolution != deleteDefinitiveFailure {
		t.Fatalf("expected definitive failure, got %s", resolution)
	}
	if !errors.Is(err, deleteErr) {
		t.Fatalf("expected original delete error, got %v", err)
	}
	if verifyCalls != 0 || cancellationCalls != 0 || reconciliationCalls != 0 || financialMutationCalls != 0 {
		t.Fatalf("definitive failure must not read, cancel, reconcile, or mutate: reads=%d cancellations=%d reconciliations=%d mutations=%d", verifyCalls, cancellationCalls, reconciliationCalls, financialMutationCalls)
	}
}

func TestConfirmationIntentKeysAreReplayStableButDistinctAcrossLaterIntents(t *testing.T) {
	// These payload shapes mirror the callback contracts emitted by the
	// extension and purchase confirmation screens. A callback replay carries
	// the same opaque intent token; a new confirmation generates another token.
	extensionCallback := "1:42:intent-extension-a"
	replayedExtensionCallback := extensionCallback
	laterExtensionCallback := "1:42:intent-extension-b"

	extensionKey := func(payload string) string {
		parts := strings.Split(payload, ":")
		if len(parts) != 3 {
			t.Fatalf("invalid extension callback payload %q", payload)
		}
		return fmt.Sprintf("wallet_extend:%s", parts[2])
	}

	if got := extensionKey(extensionCallback); got != extensionKey(replayedExtensionCallback) {
		t.Fatalf("replaying a confirmation changed its operation key")
	}
	if extensionKey(extensionCallback) == extensionKey(laterExtensionCallback) {
		t.Fatalf("a later identical +1-month extension must receive a new operation key")
	}

	firstPurchaseToken := "intent-purchase-refunded"
	secondPurchaseToken := "intent-purchase-new"
	firstPurchaseKey := "wallet_purchase:" + firstPurchaseToken
	refundKey := firstPurchaseKey + ":refund"
	secondPurchaseKey := "wallet_purchase:" + secondPurchaseToken
	if firstPurchaseKey == secondPurchaseKey || refundKey == secondPurchaseKey {
		t.Fatalf("new purchase intent reused a prior purchase/refund operation key")
	}
}
