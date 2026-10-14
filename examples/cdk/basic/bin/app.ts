#!/usr/bin/env node
import * as cdk from 'aws-cdk-lib';
import { BasicStack } from '../lib/stack';

const app = new cdk.App();
new BasicStack(app, 'HomeCloudBasic', {
  // The account and region come from the credentials (`aws sts get-caller-identity`), as on AWS.
  env: { account: process.env.CDK_DEFAULT_ACCOUNT, region: process.env.CDK_DEFAULT_REGION },
});
