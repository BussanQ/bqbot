import { readFileSync, writeFileSync } from "node:fs";

// Run in an ephemeral release checkout. No private key is written to configuration.
const config = JSON.parse(readFileSync("mygo.json", "utf8"));
const required = (name) => {
  const value = process.env[name];
  if (!value) throw new Error(`Missing release configuration: ${name}`);
  return value;
};
const tag = required("BQBOT_RELEASE_TAG");
if (tag !== `v${config.version}`) throw new Error("Release tag must match mygo.json version");
const publicKey = required("MYGO_UPDATER_PUBLIC_KEY");
if (Buffer.from(publicKey, "base64").length !== 32) throw new Error("Updater public key must be a base64 Ed25519 public key");
required("MYGO_UPDATER_PRIVATE_KEY");
config.updates = { publicKey, github: required("GITHUB_REPOSITORY"), changelog: "CHANGELOG.md", deltas: 3 };
if (process.env.BQBOT_RELEASE_PLATFORM === "darwin") {
  config.macos = { ...config.macos, signingIdentity: required("MACOS_SIGNING_IDENTITY"), notarize: { keychainProfile: required("NOTARY_PROFILE") } };
  if (process.env.NOTARY_KEYCHAIN) config.macos.notarize.keychain = process.env.NOTARY_KEYCHAIN;
}
if (process.env.BQBOT_RELEASE_PLATFORM === "windows") {
  config.windows = { ...config.windows, certificate: required("WINDOWS_CERTIFICATE_FILE") };
  required("MYGO_WINDOWS_CERTIFICATE_PASSWORD");
}
writeFileSync("mygo.json", JSON.stringify(config, null, 2) + "\n");
