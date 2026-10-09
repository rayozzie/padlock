# Padlock Security Model

Padlock splits data using random pads, XOR, and combinatorial distribution. Its confidentiality depends on the randomness used to generate the pads. The ideal threshold scheme and the default software generator have different security assumptions.

## Ideal Threshold Scheme

For each data chunk and each combination of K collections, Padlock requests K-1 pads, XORs them with the data to form the final piece, and distributes the K pieces across the combination. Any K intact collections from the same backup contain a complete combination and can recover the data.

Perfect secrecy for fewer than K collections requires every pad to be uniformly random, independent of the data and all other pads, secret from the attacker, and never reused. Under those assumptions, each incomplete combination has a missing piece that hides the data. This protects the contents, not visible metadata such as collection sizes and threshold parameters.

Underdetermined equations alone do not prove secrecy when the pads can be predicted or are correlated. The ideal proof does not establish that the default generator satisfies these assumptions.

## Default Random Number Generation

`MultiRNG` combines five generator outputs by XOR:

| Generator | Output or seed source | Limitations |
|-----------|-----------------------|-------------|
| `CryptoRand` | Calls Go's `crypto/rand` for every output buffer | Relies on the OS randomness source |
| `MathRand` | Separate eight-byte seed from `crypto/rand.Reader` | Legacy `math/rand` has fewer than 2^31 seeded states and is not a security fallback |
| `ChaCha20Rand` | Separate 32-byte key and 12-byte nonce from `crypto/rand.Reader` | Seeded software generator; reseeds before its 256 GiB stream limit |
| `PCG64Rand` | Two random 64-bit seed words from a separate 16-byte read of `crypto/rand.Reader` | Statistical PRNG; neither seed word comes from the clock |
| `MT19937Rand` | Separate eight-byte seed from `crypto/rand.Reader` | Statistical PRNG with a 64-bit seed |

These are **five algorithms sharing one OS randomness source**. Go's `crypto/rand` is the standard source for security-sensitive random bytes. Separate draws from a functioning OS RNG are intended to provide unpredictable values. Each seeded generator has separate state and consumes separate seed bytes.

The OS may combine multiple entropy inputs internally; Padlock does not separately control or verify their independence. All the generators depend on the same OS source that supplies `CryptoRand` output. Algorithm diversity does not establish independent entropy or guarantee protection if that source is compromised.

Without explicit external source options, Padlock does not separately collect hardware entropy, process IDs, timing jitter, or `/dev/urandom` bytes, and it does not hash inputs to mix them. Opening another interface to the same OS random generator would not establish an independent source. Timestamps and process IDs are not substitutes for unpredictable seed material.

`MathRand`, `PCG64Rand`, and `MT19937Rand` remain in the mixture for algorithm diversity, not as security fallbacks. A random seed does not make these statistical PRNGs secure on their own. See Go's documentation for [math/rand](https://pkg.go.dev/math/rand), [math/rand/v2](https://pkg.go.dev/math/rand/v2), and [crypto/rand](https://pkg.go.dev/crypto/rand).

### What XOR Mixing Establishes

If one input is uniform and independent of the other inputs and the attacker's knowledge, its XOR with the others remains secret and uniform. Without that condition there is no general guarantee that the result is as strong as its strongest input. For example, two identical streams cancel: `X XOR X = 0`.

The default setup therefore relies on the OS RNG and generator implementations. It does not establish information-theoretic secrecy or unconditional resistance to future classical or quantum attacks. A compromised source returning predictable bytes without an error need not be detected.

### Optional Source Diversity

`-entropy-file` and `-entropy-url` allow the operator to supplement the OS source with separately supplied raw bytes. File inputs support regular files, devices, named pipes, and stdin (`-`). HTTPS endpoints must return fresh `application/octet-stream` responses. Both options can be repeated; normal operation remains offline. See the [README source contract](../../README.md#optional-external-entropy) and [Usage Guide](Usage-Guide) for examples.

Each source contributes 76 fresh bytes at initialization. Padlock XORs every contribution with fresh OS bytes, assigns separate seed ranges to the four seeded generators, and retains the direct OS output provider. ChaCha20 reseeding consumes 44 more fresh bytes from every source and the OS. A failed or exhausted selected source aborts the operation, including during reseeding. Initial seeding occurs before output clearing. Dry runs do not consume sources, and decoding does not need them.

The independence claim is conditional: if a contribution is uniform, secret from the attacker, and independent of the others, XOR preserves those properties in the mixed seed. Extra options let the operator supply that diversity; Padlock cannot certify it merely by counting sources. Reopening the same seed file on another run reuses its bytes, so provide fresh contents for every encoding run. Padlock never rewinds or cycles a source during an operation.

Network providers know their contribution. Public beacons cannot supply secret entropy once their outputs are published. HTTPS transport uses TLS and depends on the local host and its randomness; it is not a guarantee of independence from a compromised OS RNG. External hardware or separately generated secret bytes can provide a different origin without depending on an HTTPS session. A fully compromised encoding host can undermine any of these sources.

This option adds source diversity without changing XOR splitting or the backup format. It does not make statistical PRNGs secure on their own or establish information-theoretic secrecy for output expanded from finite seeds.

### Failure Handling

- Every seed must be read completely. Initialization stops on failure rather than falling back to zero, a timestamp, or partial seed material. The optional-source CLI reports initialization errors normally; older standalone constructors panic on initialization errors. Failures in the OS RNG may terminate the process.
- ChaCha20 obtains a fresh key and nonce before it exhausts its stream. A reseeding error is returned without restarting the old stream.
- If any generator returns an error, `MultiRNG` returns the error and leaves the caller's buffer unchanged. It never silently continues with fewer generators.
- A successful read only establishes that bytes were supplied; it does not verify their unpredictability.
- Temporary seed and mixer buffers are cleared after use where Padlock owns them, and ChaCha retains only its active stream state rather than extra key/nonce slices. This is best-effort cleanup; it does not guarantee erasure of all runtime copies or protection against process compromise.

## Operational Boundaries

1. **Protect the encoding host:** The generators run in one process and share an OS dependency. Their locks protect concurrent access, not against a compromised host.
2. **Protect the collections:** Access to K intact collections permits reconstruction. Store them separately according to the intended threshold.
3. **Do not reuse pads:** Never reuse pad bytes for different data or restart generators from previously used state. New reads do not prove independent entropy, and chunk boundaries do not provide a forward-secrecy guarantee after state compromise.
4. **Expect visible metadata:** Collection names, counts, sizes, and the public backup identifier expose some information about a backup. Matching identifiers associate collections with the same encoding run.
5. **Distinguish error checks from authenticity:** Backup identifiers detect accidental mixing across encoding runs. First-chunk identifiers are compared before destination preparation, and later chunks are checked during decoding. These identifiers, CRCs, and archive validation do not authenticate contents or reliably detect deliberate alterations.

## Verification and Validation

The development tests check seed consumption, complete seed reads, failure propagation, ChaCha20 reseeding, and encode/decode behavior. Their statistical checks can catch some gross defects. Output writers do not validate randomness or issue byte-frequency quality warnings. The tests cannot certify independence, unpredictability, perfect secrecy, or resistance to attacks; deterministic generators can pass them.

The mathematical threshold argument is conditional on the ideal randomness assumptions. The default implementation has not established those information-theoretic assumptions through testing or formal verification.
