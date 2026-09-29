# Contributing to HomeCloud

Thank you for your interest in contributing to HomeCloud! Whether you're fixing bugs, adding features, improving documentation, or suggesting ideas, we appreciate your help.

## How to Get Started

1. **Fork the Repository**  
    Create a fork of this repository in your GitHub account to work on your contributions.

2. **Clone Your Fork**  
    Clone your forked repository to your local machine.
    ```bash
    git clone https://github.com/<your-username>/homecloud.git
    cd homecloud
    ```

3. **Create a Branch**  
    Create a new branch for your changes. Use a descriptive name for the branch.
    ```bash
    git checkout -b feature/my-new-feature
    ```

4. **Make Changes**  
    Implement your changes or fixes. Ensure the code adheres to the coding standards outlined below.

5. **Test Your Changes**  
    You need Go 1.23+, Node.js 20+ and Docker.
    ```bash
    make test                              # Go unit tests
    cd cli && go run . serve               # run the server (API + console on :8080)
    ./scripts/smoke.sh                     # end-to-end check of every service against the running server
    cd console && NEXT_PUBLIC_API_URL=http://127.0.0.1:8080 npm run dev   # console with hot reload
    ```
    If you add or change API routes, regenerate the reference with `python3 scripts/gen-api-docs.py`.

6. **Commit Your Changes**  
    Write clear and concise commit messages.
    ```bash
    git add .
    git commit -m "Add feature: my-new-feature"
    ```

7. **Push to Your Fork**  
    Push your changes to your forked repository.
    ```bash
    git push origin feature/my-new-feature
    ```

8. **Create a Pull Request (PR)**  
    Submit a pull request to the main repository. Include a clear description of the changes you've made and why they are important.

## Guidelines

### Code of Conduct

By participating in this project, you agree to abide by our Code of Conduct.

### Coding Standards

- Follow the conventions of the tech stack.
- Write clean, modular, and well-documented code.
- Ensure backward compatibility wherever possible.
- A new service is a package in `cli/internal/svc/<name>` with a `Routes(*httpx.Router)` method, wired in `cli/internal/server/server.go`. Every route declares its IAM action and, when it acts on one resource, that resource's ARN (`httpx.Res(...)`, or `httpx.Deferred()` plus `c.Authorize` in the handler). The server refuses to start otherwise.
- Anything that delivers to another resource (a rule target, a subscription, a trigger) must check the caller's permission on that resource when it is configured.
- Docker objects must carry `runtime.Labels(...)` so HomeCloud never touches containers it did not create.

### Issues and Discussions

- Browse existing Issues before opening a new one to avoid duplicates.
- Use the appropriate labels for categorization when creating an issue.

### Commit Messages

- Use meaningful and descriptive commit messages.
- Format: `type(scope): description`
- Examples:
  - `fix(storage): resolve upload bug`
  - `feat(dashboard): add bucket creation UI`

## Need Help?

If you’re stuck or need clarification, feel free to:

- Open a [Discussion](https://github.com/homecloudhq/homecloud/discussions).
- Reach out on [Discord](https://discord.gg/pemra9uaC9).

We’re excited to have you on board. Let’s build something great together!