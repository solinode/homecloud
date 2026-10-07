// Runs setup.sh with the action's inputs. No dependencies, so nothing to build.
const { spawnSync } = require("child_process");
const path = require("path");

const input = (name, def) => (process.env[`INPUT_${name.toUpperCase()}`] || def).trim();

const env = {
  ...process.env,
  HC_VERSION: input("version", "latest"),
  HC_PORT: input("port", "8080"),
  HC_SERVICES_WAIT: input("services-wait", ""),
  HC_WAIT_TIMEOUT: input("wait-timeout", "180"),
};
const r = spawnSync("bash", [path.join(__dirname, "setup.sh")], { stdio: "inherit", env });
if (r.error) {
  console.log(`::error::${r.error.message}`);
  process.exit(1);
}
process.exit(r.status === null ? 1 : r.status);
