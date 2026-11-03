package payments

import "testing"

func TestIdempotencyKeyIsStablePerOrder(t *testing.T) {
	first, retry := IdempotencyKey("1001"), IdempotencyKey("1001")
	if first != retry {
		t.Fatal("retries of the same order must reuse the same key")
	}
	if first == IdempotencyKey("1002") {
		t.Fatal("different orders must get different keys")
	}
}
