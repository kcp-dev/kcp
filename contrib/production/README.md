# Production Deployment Assets

This directory contains assets and configuration files for production deployment of kcp.

!!! Note: We understand that maintaining static assets in the repository can be challenging. If you have noticed any discrepancies between these assets and the latest version of the kcp - please open an issue or submit a pull request to help us keep them up to date.

## Usage

These assets are referenced by the production deployment documentation in `docs/content/setup/production/`.

Each deployment type (dekker, vespucci, comer) has its own subdirectory with complete configuration files and deployment manifests.

## Deployment Types

- **kcp-dekker**: Self-signed certificates, simple single-cluster deployment
- **kcp-vespucci**: External certificates with Let's Encrypt, public shard access  
- **kcp-comer**: CDN integration with dual front-proxy configuration

See the corresponding documentation in `docs/content/setup/production/` for detailed deployment instructions.