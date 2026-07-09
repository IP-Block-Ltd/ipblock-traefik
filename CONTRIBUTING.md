# Contributing

Thanks for your interest in improving this IP Block extension!

> **Heads up:** this extension is currently **untested in production**. Bug reports, fixes, and improvements are very welcome.

## How to contribute
1. Fork the repository and create a feature branch.
2. Follow the coding conventions of the target platform — this extension mirrors the platform's native structure.
3. Keep the ip-block.com API contract intact: `POST https://api.ip-block.com/v1/check` with `{api_key, site_id, ip, user_agent, referrer}` → `{"action":"allow"|"block"}`, a ~1-second timeout, and **fail-open** on any error.
4. Test your change against a local install of the platform where possible.
5. Open a pull request describing what you changed and why.

## Reporting issues
Open a GitHub issue with the platform version, the extension version, and clear steps to reproduce.

## Licence
By contributing you agree that your contributions are licensed under the **GNU GPLv3**, the same licence as this project.
