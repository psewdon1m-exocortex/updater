import fs from "node:fs";
import path from "node:path";
import { createHash, generateKeyPairSync } from "node:crypto";

const [service, directory] = process.argv.slice(2);
if (!["kernel", "volt", "updater", "neptune", "gryphon"].includes(service) || !directory) throw new Error("Usage: create-release-key.mjs SERVICE PROTECTED_DIRECTORY");
fs.mkdirSync(directory, { recursive: true, mode: 0o700 });
const privatePath = path.join(directory, `${service}.private.pem`), publicPath = path.join(directory, `${service}.pem`);
if (fs.existsSync(privatePath) || fs.existsSync(publicPath)) throw new Error("Release key files already exist; rotation requires an explicit separate destination");
const { privateKey, publicKey } = generateKeyPairSync("rsa", { modulusLength: 3072 });
fs.writeFileSync(privatePath, privateKey.export({ type: "pkcs8", format: "pem" }), { flag: "wx", mode: 0o600 });
fs.writeFileSync(publicPath, publicKey.export({ type: "spki", format: "pem" }), { flag: "wx", mode: 0o644 });
process.stdout.write(JSON.stringify({ service, public_key: path.resolve(publicPath), key_id: createHash("sha256").update(publicKey.export({ type: "spki", format: "der" })).digest("hex") }) + "\n");
