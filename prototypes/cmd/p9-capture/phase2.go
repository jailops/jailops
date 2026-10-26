package main

import (
	"fmt"

	"jailops.net/jailops/prototypes/internal/capture"
	"jailops.net/jailops/prototypes/internal/harness"
)

const fuzzBatch = 15

// test2 settles section 16 row 10: does `zfs diff -FHt` agree with a full L0 stat-walk over a
// randomized operation sequence (design 9.4 step 4's fuzz test)?
func test2(e *harness.Env, root string) {
	e.Step("2. L0 stat-walk vs `zfs diff -FHt` equivalence (row 10), seed=%d ops=%d", seed, fuzzOps)
	fuzz := root + "/fuzz"
	e.Must("create fuzz dataset", e.Zfs("create", "-o", "mountpoint="+mp("fuzz"), fuzz).OK(), "dataset for the randomized operation fuzz test")
	dir := mp("fuzz")

	ops := capture.GenOps(seed, fuzzOps)
	prevTree, err := WalkTree(dir)
	if err != nil && !e.DryRun {
		e.Abort("walk fuzz tree: %v", err)
	}
	e.Run("zfs", "snapshot", fuzz+"@f0")
	prevSnap := "f0"

	disagreements := 0
	batches := 0
	for start := 0; start < len(ops); start += fuzzBatch {
		end := min(start+fuzzBatch, len(ops))
		batch := ops[start:end]
		batches++
		if e.DryRun {
			e.Note("would apply %d operations (batch %d)", len(batch), batches)
		} else {
			for _, op := range batch {
				if err := applyOp(dir, op); err != nil {
					e.Note("op %+v failed (non-fatal, recorded): %v", op, err)
				}
			}
		}
		curSnap := fmt.Sprintf("f%d", batches)
		curTree, err := WalkTree(dir)
		if err != nil && !e.DryRun {
			e.Abort("walk fuzz tree: %v", err)
		}
		e.Run("zfs", "snapshot", fuzz+"@"+curSnap)

		if e.DryRun {
			continue
		}
		l0 := capture.Compare(prevTree, curTree)
		diffOut := e.Quiet("zfs", "diff", "-FHt", fuzz+"@"+prevSnap, fuzz+"@"+curSnap).Out()
		zdOps, perr := capture.ParseZfsDiff(diffOut)
		if perr != nil {
			e.Check(fmt.Sprintf("2 batch %d: zfs diff parses", batches), false, perr.Error())
			disagreements++
		} else {
			zd := capture.ZfsDiffChangeSetRel(zdOps, mp("fuzz"))
			if !l0.Equal(zd) {
				disagreements++
				e.Check(fmt.Sprintf("2 batch %d: L0 agrees with zfs diff", batches), false,
					fmt.Sprintf("ops=%v disagreements=%v", batch, l0.Disagreements(zd)))
			} else {
				e.Check(fmt.Sprintf("2 batch %d: L0 agrees with zfs diff", batches), true,
					fmt.Sprintf("added=%d removed=%d modified=%d", len(l0.Added), len(l0.Removed), len(l0.Modified)))
			}
		}
		prevTree, prevSnap = curTree, curSnap
	}
	e.Check("L0 and zfs diff agreed on every batch", disagreements == 0 || e.DryRun,
		fmt.Sprintf("%d/%d batches disagreed (seed=%d, rerun with the same --seed to reproduce)", disagreements, batches, seed))
	e.Resolve(fmt.Sprintf("zfs diff -FHt vs full L0 walk equivalence over %d randomized operations in %d batches: disagreements=%d", len(ops), batches, disagreements))
}
