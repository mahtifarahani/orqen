import { defineConfig } from 'orval'

// Generated from web/openapi.json (produced by `make openapi` from api/openapi.json embedded in the Go server).
export default defineConfig({
  orqen: {
    input: { target: './openapi.json' },
    output: {
      target: './src/api/generated/endpoints.ts',
      schemas: './src/api/generated/model',
      client: 'react-query',
      httpClient: 'fetch',
      mode: 'tags-split',
      clean: true,
      prettier: false,
      override: {
        mutator: { path: './src/api/mutator.ts', name: 'orqenFetch' },
        query: { version: 5, useInfinite: true, useInfiniteQueryParam: 'cursor' },
      },
    },
  },
})
