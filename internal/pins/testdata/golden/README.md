# Golden files for internal/pins

| File | What it is |
|---|---|
| `ingredients.txt` | `Ingredients()`: every registry row with three or more fields and a name, every component version and config.plist's checksum, as `name<TAB>value`, sorted bytewise |
| `ingredients-digest.txt` | `Digest` over those rows: the ingredient digest, one sha256 standing for every pin |

Used by `TestIngredientsMatchTheirGolden` in `pins_test.go`;
`TestTheIngredientDigestIsPinned` holds the same digest as a constant.

## When one changes

Both change whenever a pin does -- a row of `assets/pins/sources.tsv`, a
`components/*/version`, or `assets/firmware/config.plist` -- and never
otherwise. Update `ingredients.txt`, `ingredients-digest.txt` and the
constant in `TestTheIngredientDigestIsPinned` in the same commit as the
pin, and say which pin moved in its message. `bin/pins-bot.sh sync
<base-ref>` does that for every pin moved since `<base-ref>`, and the
listing goldens in `internal/inputs/testdata/golden` with it; on a
Renovate branch `.github/workflows/pins-bot.yml` runs it for you.
