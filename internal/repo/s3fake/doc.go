// Package s3fake is an in-process S3 server for tests: standard library only,
// one loopback listener, memory storage.
//
// It is strict. It implements exactly the S3 behaviour the repository relies
// on, as AWS S3 behaves, and answers 501 NotImplemented to everything else, so
// a code change that starts using another feature fails loudly instead of being
// half served:
//
//   - path-style and virtual-host style addressing (the latter when Options.Domain is set);
//   - AWS Signature V4, header form, verified from the specification: unknown
//     keys, wrong secrets, tampered requests, unsigned x-amz-* headers and
//     clock skew are refused with the S3 error codes. The payload hash is either
//     UNSIGNED-PAYLOAD or a hex SHA-256 that is checked against the body;
//   - buckets: create, head, delete;
//   - objects: single-part PutObject with an atomic If-None-Match: * (412
//     PreconditionFailed), GetObject with Range, HeadObject, idempotent
//     DeleteObject, ListObjectsV2 (prefix, continuation tokens, start-after,
//     max-keys, url encoding). A torn upload never leaves an object behind;
//   - multipart upload, copy, versioning, delimiters, conditional reads and
//     every other feature answer 501.
//
// For tests it injects faults (an error on the Nth request, SlowDown bursts,
// latency, connections dropped mid-body or instead of the answer, requests that
// hang), logs every request and counts them. Quirks switch single defects on so
// that the conformance suite can be shown to fail against a wrong server.
package s3fake
