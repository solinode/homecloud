// Post step (every job): runs post.sh with the state setup.sh saved.
const { spawnSync } = require("child_process");
const path = require("path");

const env = {
  ...process.env,
  HC_LOG: process.env.STATE_log || "",
  HC_PID: process.env.STATE_pid || "",
  HC_DATA: process.env.STATE_data || "",
};
const r = spawnSync("bash", [path.join(__dirname, "post.sh")], { stdio: "inherit", env });
if (r.error) console.log(`::warning::HomeCloud clean-up: ${r.error.message}`);
// Clean-up problems never fail the job.
process.exit(0);
