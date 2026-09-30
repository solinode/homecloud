import type { DemoService } from "../engine"

// TODO(demo): fixtures + routes for lambda
const service: DemoService = {
  name: "lambda",
  seed: () => ({}),
  routes: () => {},
}

export default service
