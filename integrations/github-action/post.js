// Post step (runs only when the job failed): print the HomeCloud server log.
const fs = require("fs");

const log = process.env.STATE_log;
if (!log || !fs.existsSync(log)) {
  console.log("HomeCloud server log not found");
  process.exit(0);
}
console.log("::group::HomeCloud server log");
process.stdout.write(fs.readFileSync(log, "utf8"));
console.log("::endgroup::");
