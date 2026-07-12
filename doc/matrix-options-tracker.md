# Proposal: Discover Formula Matrix with SSA Instrumentation

## Summary

LLAR discovers matrix keys by observing formula execution. Formula code remains
unchanged; matrix reads are instrumented in Go SSA after ixgo has built the
package and before ixgo translates it for execution.

## Pipeline

```text
_llar.gox source
  -> generated Go source
  -> x/tools SSA and built-in optimization
  -> tracker call insertion
  -> ixgo interpreter
```

The tracker does not rewrite XGo AST or modify ixgo.

## Instrumentation

XGo lowers a matrix read into a method call followed by `ssa.Lookup`:

```text
target.options[key]
  -> matrixTarget.Options()
  -> map lookup
```

The tracker inserts no-result calls at two points:

1. After `matrixTarget.Options()` or `matrixTarget.Require()`, register the
   returned map as options or require.
2. Before each `ssa.Lookup` on `map[string][]string`, record the key only when
   the map was registered in step 1.

Map identity follows normal assignments and function arguments, so aliases and
helpers do not require data-flow reconstruction.

## Probe

`LoadFS` executes the formula hooks with empty probe inputs, then copies the
observed require and options keys into `Formula.Matrix`. Values are filled later
from the selected matrix. Tracker calls are disabled after the probe and do not
affect normal builds.

## Boundaries

- Instrumentation runs after all x/tools SSA builder passes.
- Inserted calls do not produce values or change control flow.
- Only code represented in the ixgo SSA program can be instrumented.
- Additional probe rounds and candidate generation are separate concerns.
