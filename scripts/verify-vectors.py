#!/usr/bin/env python3
"""Independent check of the repository format vectors.

Re-implements the constructions of internal/repo/format/doc.go from the byte
layouts alone (it does not use the Go code): HKDF-SHA256, HMAC-SHA256,
SHA-256, XChaCha20-Poly1305 (HChaCha20, ChaCha20 and Poly1305 written out in
pure Python from RFC 8439 and draft-irtf-cfrg-xchacha) and the catalog entry
encoding. It needs only the standard library.

Not covered: zstd decompression of the compressed chunk body (the Python
standard library of 3.13 has no zstd; the script decrypts the body and checks
the frame magic), the Argon2id key file and the age escrow blob.

    python3 scripts/verify-vectors.py
"""
import hashlib
import hmac
import json
import os
import struct
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FORMAT_VECTORS = os.path.join(ROOT, "internal/repo/format/testdata/vectors.json")
CATALOG_VECTORS = os.path.join(ROOT, "internal/repo/catalog/testdata/vectors.json")
CHUNKER_VECTORS = os.path.join(ROOT, "internal/repo/chunker/testdata/vectors.json")

M32 = 0xFFFFFFFF


def rotl(v, n):
    return ((v << n) & M32) | (v >> (32 - n))


def qr(s, a, b, c, d):
    s[a] = (s[a] + s[b]) & M32; s[d] = rotl(s[d] ^ s[a], 16)
    s[c] = (s[c] + s[d]) & M32; s[b] = rotl(s[b] ^ s[c], 12)
    s[a] = (s[a] + s[b]) & M32; s[d] = rotl(s[d] ^ s[a], 8)
    s[c] = (s[c] + s[d]) & M32; s[b] = rotl(s[b] ^ s[c], 7)


def rounds(s):
    for _ in range(10):
        qr(s, 0, 4, 8, 12); qr(s, 1, 5, 9, 13); qr(s, 2, 6, 10, 14); qr(s, 3, 7, 11, 15)
        qr(s, 0, 5, 10, 15); qr(s, 1, 6, 11, 12); qr(s, 2, 7, 8, 13); qr(s, 3, 4, 9, 14)


CONST = struct.unpack("<4I", b"expand 32-byte k")


def hchacha20(key, nonce16):
    s = list(CONST) + list(struct.unpack("<8I", key)) + list(struct.unpack("<4I", nonce16))
    rounds(s)
    return struct.pack("<8I", *(s[0:4] + s[12:16]))


def chacha20_block(key, counter, nonce12):
    init = list(CONST) + list(struct.unpack("<8I", key)) + [counter] + list(struct.unpack("<3I", nonce12))
    s = init[:]
    rounds(s)
    return struct.pack("<16I", *[(a + b) & M32 for a, b in zip(s, init)])


def chacha20_xor(key, counter, nonce12, data):
    out = bytearray()
    for i in range(0, len(data), 64):
        ks = chacha20_block(key, counter + i // 64, nonce12)
        out += bytes(a ^ b for a, b in zip(data[i:i + 64], ks))
    return bytes(out)


def poly1305(key, msg):
    r = int.from_bytes(key[:16], "little") & 0x0ffffffc0ffffffc0ffffffc0fffffff
    s = int.from_bytes(key[16:], "little")
    p = (1 << 130) - 5
    acc = 0
    for i in range(0, len(msg), 16):
        blk = msg[i:i + 16] + b"\x01"
        acc = ((acc + int.from_bytes(blk, "little")) * r) % p
    return ((acc + s) & ((1 << 128) - 1)).to_bytes(16, "little")


def pad16(b):
    return b + b"\0" * (-len(b) % 16)


def xchacha_open(key, nonce24, aad, ct_and_tag):
    subkey = hchacha20(key, nonce24[:16])
    n12 = b"\0\0\0\0" + nonce24[16:]
    ct, tag = ct_and_tag[:-16], ct_and_tag[-16:]
    otk = chacha20_block(subkey, 0, n12)[:32]
    mac = poly1305(otk, pad16(aad) + pad16(ct) + struct.pack("<QQ", len(aad), len(ct)))
    if not hmac.compare_digest(mac, tag):
        return None
    return chacha20_xor(subkey, 1, n12, ct)


def hkdf(master, salt, info, length=32):
    prk = hmac.new(salt, master, hashlib.sha256).digest()
    out, t, i = b"", b"", 1
    while len(out) < length:
        t = hmac.new(prk, t + info + bytes([i]), hashlib.sha256).digest()
        out += t
        i += 1
    return out[:length]


ok = True


def check(name, cond):
    global ok
    print(("PASS " if cond else "FAIL ") + name)
    ok = ok and bool(cond)


def uvarint(b, pos):
    v, shift = 0, 0
    while True:
        c = b[pos]; pos += 1
        v |= (c & 0x7F) << shift
        if c < 0x80:
            return v, pos
        shift += 7


def varint(b, pos):
    u, pos = uvarint(b, pos)
    return (u >> 1) ^ -(u & 1), pos


def b32decode(s):
    import base64
    return base64.b32decode(s + "=" * (-len(s) % 8))


def verify_format(path):
    v = json.load(open(path))
    master = bytes.fromhex(v["master_hex"])
    salt = b"JailOps/backup/v1"
    k_enc = hkdf(master, salt, b"chunk-encryption")
    k_id = hkdf(master, salt, b"chunk-id")
    k_man = hkdf(master, salt, b"manifest-encryption")
    k_dom = hkdf(master, salt, b"domain-id")
    check("k_enc", k_enc.hex() == v["k_enc_hex"])
    check("k_id", k_id.hex() == v["k_id_hex"])
    check("k_manifest", k_man.hex() == v["k_manifest_hex"])
    check("domain id = first 8 bytes of HKDF(domain-id)", k_dom[:8].hex() == v["domain_id"])
    domain = k_dom[:8]

    def check_chunk(tag, plain_hex, nonce_hex, id_hex, blob_hex, compressed):
        plain = bytes.fromhex(plain_hex)
        cid = hmac.new(k_id, plain, hashlib.sha256).digest()
        check(tag + " chunk id = HMAC-SHA256(k_id, plaintext)", cid.hex() == id_hex)
        blob = bytes.fromhex(blob_hex)
        check(tag + " blob trailer = SHA-256(preceding bytes)", hashlib.sha256(blob[:-32]).digest() == blob[-32:])
        check(tag + " blob magic/version/flags/reserved",
              blob[:4] == b"JOCH" and blob[4] == 1 and blob[5] == (1 if compressed else 0) and blob[6:8] == b"\0\0")
        nonce, ct = blob[8:32], blob[32:-32]
        check(tag + " nonce as recorded", nonce.hex() == nonce_hex)
        aad = blob[:8] + domain + cid
        pt = xchacha_open(k_enc, nonce, aad, ct)
        check(tag + " blob authenticates (XChaCha20-Poly1305, aad = header|domain|id)", pt is not None)
        if pt is not None:
            if compressed:
                check(tag + " body is a zstd frame (content not decompressed here)", pt[:4] == bytes.fromhex("28b52ffd"))
            else:
                check(tag + " blob decrypts to the plaintext", pt == plain)
        wrong = xchacha_open(k_enc, nonce, blob[:8] + domain + bytes(32), ct)
        check(tag + " wrong id in the aad is rejected", wrong is None)

    check_chunk("plain", v["chunk_plaintext_hex"], v["chunk_nonce_hex"], v["chunk_id_hex"], v["chunk_blob_hex"], False)
    check_chunk("zstd", v["zchunk_plaintext_hex"], v["zchunk_nonce_hex"], v["zchunk_id_hex"], v["zchunk_blob_hex"], True)

    group = hmac.new(k_id, b"group\0" + v["group_host"].encode() + b"\0" + v["group_jail"].encode(), hashlib.sha256).hexdigest()[:32]
    check("group id = HMAC(k_id, 'group\\0'|host|'\\0'|jail)[:16]", group == v["group_id"])

    idx = bytes.fromhex(v["index_file_hex"])
    check("index trailer", hashlib.sha256(idx[:-32]).digest() == idx[-32:])
    check("index header (JOIX, v1, kind 2, reserved 0)", idx[:8] == b"JOIX\x01\x02\x00\x00")
    total, count = struct.unpack(">QQ", idx[8:24])
    ents = [(struct.unpack(">Q", idx[24 + i * 40:32 + i * 40])[0], idx[32 + i * 40:64 + i * 40]) for i in range(count)]
    check("index length = 24 + 40*count + 32", len(idx) == 24 + 40 * count + 32)
    check("index ends strictly increase and last end = total", all(ents[i][0] > (ents[i - 1][0] if i else 0) for i in range(count)) and ents[-1][0] == total)
    check("index entry 0 = plain chunk id, entry 1 = zero id, entry 2 = zstd chunk id",
          ents[0][1].hex() == v["chunk_id_hex"] and ents[1][1] == bytes(32) and ents[2][1].hex() == v["zchunk_id_hex"])
    check("index sizes = plaintext sizes", ents[0][0] == len(v["chunk_plaintext_hex"]) // 2 and total == ents[0][0] + 100 + len(v["zchunk_plaintext_hex"]) // 2)

    mb = bytes.fromhex(v["manifest_blob_hex"])
    check("manifest trailer", hashlib.sha256(mb[:-32]).digest() == mb[-32:])
    check("manifest header", mb[:8] == b"JOMF\x01\x00\x00\x00")
    mj = json.loads(v["manifest_json"])
    snap = mj["snapshot_id"]
    maad = b"JOMF" + bytes([1, 0, 0, 0]) + v["group_id"].encode() + b"\0" + snap.encode()
    pt = xchacha_open(k_man, mb[8:32], maad, mb[32:-32])
    check("manifest authenticates with aad = header|group|0|snapshot", pt is not None)
    if pt is not None:
        check("manifest decrypts to the recorded JSON", json.loads(pt) == mj)
    check("manifest does not authenticate for another snapshot",
          xchacha_open(k_man, mb[8:32], maad[:-1] + b"0", mb[32:-32]) is None)
    check("manifest index digest = SHA-256(index file)", mj["index_digest"] == hashlib.sha256(idx).hexdigest())
    check("manifest streams tile the index", sum(s["payload_size"] + s["catalog_size"] for s in mj["streams"]) == total)

    cf = bytes.fromhex(v["catalog_frame_hex"])
    check("catalog frame = JOCT 01 000000 | body | SHA-256", cf[:8] == b"JOCT\x01\0\0\0" and cf[8:-32].hex() == v["catalog_body_hex"] and hashlib.sha256(cf[:-32]).digest() == cf[-32:])

    rc = json.loads(v["receipt_json"])
    check("receipt digests", rc["index_digest"] == hashlib.sha256(idx).hexdigest() and rc["manifest_digest"] == hashlib.sha256(mb).hexdigest() and rc["index_bytes"] == len(idx))

    rk = v["recovery_key"]
    check("recovery key prefix and groups", rk.startswith("JOR1-"))
    raw = b32decode(rk[5:].replace("-", ""))
    check("recovery key payload = master", raw[:32] == master)
    check("recovery checksum", raw[32:] == hashlib.sha256(b"JailOps backup recovery key v1" + master).digest()[:4])


M64 = (1 << 64) - 1


def splitmix_next(x):
    x = (x + 0x9E3779B97F4A7C15) & M64
    z = x
    z = ((z ^ (z >> 30)) * 0xBF58476D1CE4E5B9) & M64
    z = ((z ^ (z >> 27)) * 0x94D049BB133111EB) & M64
    return x, z ^ (z >> 31)


def gear_table():
    x = 0x4A61696C4F70732F  # "JailOps/"
    t = []
    for _ in range(256):
        x, z = splitmix_next(x)
        t.append(z)
    return t


def corpus(n, seed):
    x, out = seed, bytearray()
    while len(out) < n:
        x, z = splitmix_next(x)
        out += z.to_bytes(8, "little")
    return bytes(out[:n])


def fastcdc_cuts(data, mn, avg, mx, gear):
    bits = avg.bit_length() - 1
    mask = lambda b: ((1 << b) - 1) << (64 - b)
    ms, ml = mask(bits + 2), mask(bits - 2)
    cuts, pos, n = [], 0, len(data)
    while pos < n:
        rem = n - pos
        if rem <= mn:
            cuts.append(rem); break
        lim = min(mx, rem)
        normal = min(avg, lim)
        h = 0
        i = mn
        for j in range(max(0, i - 64), i):
            h = ((h << 1) + gear[data[pos + j]]) & M64
        cut = None
        while i < normal:
            h = ((h << 1) + gear[data[pos + i]]) & M64
            i += 1
            if h & ms == 0:
                cut = i; break
        if cut is None:
            while i < lim:
                h = ((h << 1) + gear[data[pos + i]]) & M64
                i += 1
                if h & ml == 0:
                    cut = i; break
        if cut is None:
            cut = lim
        cuts.append(cut); pos += cut
    return cuts


def fnv32a(b):
    h = 0x811C9DC5
    for c in b:
        h = ((h ^ c) * 0x01000193) & M32
    return h


def verify_chunker(path):
    v = json.load(open(path))
    g = gear_table()
    check("gear table first values", [format(x, "016x") for x in g[:3]] == v["gear_first_hex"])
    check("gear table last value", format(g[255], "016x") == v["gear_255_hex"])
    check("gear table digest", hashlib.sha256(b"".join(x.to_bytes(8, "little") for x in g)).hexdigest() == v["gear_table_sha256"])
    data = corpus(v["corpus_len"], v["corpus_seed"])
    check("corpus digest", hashlib.sha256(data).hexdigest() == v["corpus_sha256"])
    check("FastCDC cut lengths (256 KiB / 2 MiB / 8 MiB)", fastcdc_cuts(data, 256 << 10, 2 << 20, 8 << 20, g) == v["cut_lengths"])
    small = corpus(v["small_corpus_len"], v["small_corpus_seed"])
    check("FastCDC cut lengths (small parameters)", fastcdc_cuts(small, v["small_min"], v["small_avg"], v["small_max"], g) == v["small_cut_lengths"])
    check("FNV-1a-32 of pack names", all(fnv32a(k.encode()) == h for k, h in v["fnv32a"].items()))


def decode_catalog(body):
    """Decode the entry encoding of internal/repo/catalog/doc.go; returns a list of (path, summary)."""
    pos = [0]

    def u():
        v, pos[0] = uvarint(body, pos[0]); return v

    def i():
        v, pos[0] = varint(body, pos[0]); return v

    def s():
        n = u(); r = body[pos[0]:pos[0] + n]; pos[0] += n; return r

    out = []

    def entry(kind, path):
        name = s().decode()
        full = (path + "/" + name) if path and name else (name or path)
        mode, uid, gid = u(), u(), u()
        mtime, ctime, ino, flags = i(), i(), u(), u()
        attrs = []
        for _ in range(u()):
            an = s().decode(); av = s(); attrs.append((an, av))
        rec = {"kind": kind, "path": full, "mode": mode, "uid": uid, "gid": gid, "mtime": mtime, "ctime": ctime,
               "inode": ino, "flags": flags, "attrs": attrs}
        out.append(rec)
        if kind == 1:
            while True:
                k = body[pos[0]]; pos[0] += 1
                if k == 0:
                    break
                entry(k, full)
        elif kind == 2:
            rec["size"] = u()
            if rec["size"] > 0:
                rec["payoff"] = u(); rec["sum"] = body[pos[0]:pos[0] + 32]; pos[0] += 32
        elif kind in (3, 4):
            rec["link"] = s().decode()
        elif kind in (5, 6):
            rec["major"], rec["minor"] = u(), u()

    k = body[0]; pos[0] = 1
    entry(k, "")
    check("catalog body fully consumed", pos[0] == len(body))
    return out


def verify_catalog(path):
    v = json.load(open(path))
    frame = bytes.fromhex(v["catalog_hex"])
    check("catalog trailer = SHA-256(preceding)", frame[:8] == b"JOCT\x01\0\0\0" and hashlib.sha256(frame[:-32]).digest() == frame[-32:])
    ents = decode_catalog(frame[8:-32])
    paths = [e["path"] for e in ents]
    check("catalog paths as recorded", paths == v["paths"])
    by = {e["path"]: e for e in ents}
    for p, hexsum in v["file_sums"].items():
        e = by[p]
        data = bytes.fromhex(v["file_content_hex"][p])
        check("catalog sha-256 of " + p, e["sum"].hex() == hexsum == hashlib.sha256(data).hexdigest() and e["size"] == len(data))
    check("catalog attribute blobs as recorded", all([(n, b.hex()) for n, b in by[p]["attrs"]] == [tuple(x) for x in a] for p, a in v["attrs"].items()))
    check("catalog hardlink target", by[v["hardlink"]["path"]]["link"] == v["hardlink"]["target"])


if __name__ == "__main__":
    verify_format(sys.argv[1] if len(sys.argv) > 1 else FORMAT_VECTORS)
    if os.path.exists(CHUNKER_VECTORS):
        verify_chunker(CHUNKER_VECTORS)
    cp = sys.argv[2] if len(sys.argv) > 2 else CATALOG_VECTORS
    if os.path.exists(cp):
        verify_catalog(cp)
    else:
        print("SKIP catalog vectors (not present yet)")
    sys.exit(0 if ok else 1)
