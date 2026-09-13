import { createPublicKey } from "node:crypto";
import fs from "node:fs";

const [templatePath, publicKeyPath, outputPath, version] = process.argv.slice(2);
if (!templatePath || !publicKeyPath || !outputPath || !/^\d+\.\d+\.\d+$/.test(version ?? "")) {
  throw new Error("Usage: build-bootstrap.mjs <template> <public-key.pem> <output> <version>");
}
const publicPem = fs.readFileSync(publicKeyPath, "utf8");
const publicKey = createPublicKey(publicPem);
if (publicKey.asymmetricKeyType !== "rsa" || (publicKey.asymmetricKeyDetails?.modulusLength ?? 0) < 3072) {
  throw new Error("Updater bootstrap requires an RSA public key with at least 3072 bits");
}
if (/PRIVATE KEY/.test(publicPem)) throw new Error("Refusing to embed private key material");
const template = fs.readFileSync(templatePath, "utf8");
for (const placeholder of ["__UPDATER_BOOTSTRAP_RELEASE_VERSION__", "__UPDATER_BOOTSTRAP_PUBLIC_KEY_BASE64__"]) {
  if (template.split(placeholder).length !== 2) throw new Error(`Expected exactly one ${placeholder} placeholder`);
}
const output = template
  .replace("__UPDATER_BOOTSTRAP_RELEASE_VERSION__", version)
  .replace("__UPDATER_BOOTSTRAP_PUBLIC_KEY_BASE64__", Buffer.from(publicPem).toString("base64"));
if (output.includes("__UPDATER_BOOTSTRAP_") || /PRIVATE KEY/.test(output)) throw new Error("Unsafe generated bootstrap");
fs.writeFileSync(outputPath, output, { mode: 0o755 });
