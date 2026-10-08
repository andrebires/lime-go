# Browser patch engine provenance

`json-patch.js` is generated from the same owned-tree RFC 6902 implementation as
lime-js, avoiding a second handwritten patch algorithm in this demo. The Apache-2.0
licensed source is `andrebires/lime-js/src/Lime/Protocol/JsonPatch.ts` and its
`has` import from `Validation.ts`. The source is verified in lime-js; browser contracts and shared vectors
exercise the generated code under this repository's changed-line coverage gate.

Source SHA-256: `aa1a25be5c59ee164c8fd6538f3ca15825b008190c478e609e4f81725759448d`.

Regenerate from a matching lime-js checkout after `npm ci`:

```sh
node_modules/.bin/esbuild src/Lime/Protocol/JsonPatch.ts --bundle --format=iife \
  --global-name=LimeJsonPatch --target=es2018 --outfile=/path/to/lime-go/examples/lime2-demo/json-patch.js \
  --banner:js='// Generated from lime-js JsonPatch.ts; see json-patch-source.md for provenance.'
```
