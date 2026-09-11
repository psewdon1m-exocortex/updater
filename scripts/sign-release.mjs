import fs from "node:fs";
import path from "node:path";
import { constants, createHash, createPrivateKey, createPublicKey, sign, verify } from "node:crypto";

const pem = process.env.RELEASE_SIGNING_KEY_FILE
  ? fs.readFileSync(process.env.RELEASE_SIGNING_KEY_FILE, "utf8")
  : process.env.RELEASE_SIGNING_KEY;
if (!pem) throw new Error("A release signing key must be provisioned in the protected CI environment");
const key = createPrivateKey(pem);
if (key.asymmetricKeyType !== "rsa" || key.asymmetricKeyDetails.modulusLength < 3072) throw new Error("Release signing requires RSA with at least 3072 bits");
const publicKey = createPublicKey(key);
const keyId = createHash("sha256").update(publicKey.export({ type: "spki", format: "der" })).digest("hex");
const files = process.argv.slice(2);
const exportPublic = files[0] === "--export-public-key";
if (exportPublic) {
  const target = files[1];
  if (!target) throw new Error("Specify the public key destination");
  fs.mkdirSync(path.dirname(target), { recursive: true });
  fs.writeFileSync(target, publicKey.export({ type: "spki", format: "pem" }), { mode: 0o644 });
  files.splice(0, 2);
}
if (!files.length && !exportPublic) throw new Error("Specify one or more release manifest files");
for (const filename of files) {
  const bytes = fs.readFileSync(filename);
  if (bytes.length > 2 * 1024 * 1024) throw new Error("Release manifest exceeds the size limit");
  JSON.parse(bytes.toString("utf8"));
  const signature = sign("sha256", bytes, { key, padding: constants.RSA_PKCS1_PSS_PADDING, saltLength: 32 });
  if (!verify("sha256", bytes, { key: publicKey, padding: constants.RSA_PKCS1_PSS_PADDING, saltLength: 32 }, signature)) throw new Error("Release signature self-check failed");
  fs.writeFileSync(filename + ".sig.json", JSON.stringify({ schema: "exocortex.release-signature.v1", algorithm: "RSA-PSS-SHA256", key_id: keyId, signature: signature.toString("base64") }) + "\n");
  process.stdout.write(`Signed ${path.basename(filename)} with key ${keyId}\n`);
}
