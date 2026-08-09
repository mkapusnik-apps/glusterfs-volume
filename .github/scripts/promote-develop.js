'use strict';

const HEAD = 'develop';
const BASE = 'master';
const TITLE = 'Promote develop to master';

module.exports = async ({ github, context, core }) => {
  if (context.ref !== `refs/heads/${HEAD}`) {
    throw new Error(`Ref ${context.ref} is not the develop branch`);
  }

  const owner = context.repo.owner;
  const repo = context.repo.repo;
  const publishedSha = context.sha;
  const body = [
    'Automated promotion after complete develop plugin publication.',
    '',
    `Successfully published develop SHA: \`${publishedSha}\``,
  ].join('\n');

  const getDevelopSha = async () => {
    const branch = await github.rest.repos.getBranch({ owner, repo, branch: HEAD });
    return branch.data.commit.sha;
  };

  const stillCurrent = async (purpose) => {
    const currentSha = await getDevelopSha();
    if (currentSha === publishedSha) {
      return true;
    }
    core.notice(
      `Skipping stale ${purpose} for ${publishedSha}; develop is now ${currentSha}`,
    );
    return false;
  };

  const listExactOpenPulls = async () => {
    const response = await github.rest.pulls.list({
      owner,
      repo,
      state: 'open',
      head: `${owner}:${HEAD}`,
      base: BASE,
      per_page: 100,
    });
    if (response.data.length > 1) {
      throw new Error(
        `Found ${response.data.length} open ${HEAD} to ${BASE} pull requests; refusing ambiguous reuse`,
      );
    }
    return response.data;
  };

  const validateExactPull = (pullRequest) => {
    const expectedRepository = `${owner}/${repo}`.toLowerCase();
    const actualRepository = pullRequest.head.repo?.full_name?.toLowerCase();
    if (
      pullRequest.head.ref !== HEAD
      || pullRequest.base.ref !== BASE
      || actualRepository !== expectedRepository
    ) {
      throw new Error(`Pull request #${pullRequest.number} is not the exact ${HEAD} to ${BASE} pair`);
    }
    if (pullRequest.draft) {
      throw new Error(
        `Existing ${HEAD} to ${BASE} pull request #${pullRequest.number} is a draft; `
        + 'refusing to make it promotable',
      );
    }
  };

  if (!await stillCurrent('promotion comparison')) {
    return 'stale';
  }

  const comparison = await github.rest.repos.compareCommitsWithBasehead({
    owner,
    repo,
    basehead: `${BASE}...${publishedSha}`,
  });
  if (comparison.data.ahead_by === 0) {
    core.info(`${HEAD} has no changes to promote to ${BASE}`);
    return 'no-diff';
  }

  if (!await stillCurrent('pull request operation')) {
    return 'stale';
  }

  let exactPulls = await listExactOpenPulls();
  let pullRequest;
  if (exactPulls.length === 1) {
    pullRequest = exactPulls[0];
    validateExactPull(pullRequest);
    if (pullRequest.head.sha !== publishedSha) {
      core.notice(
        `Skipping stale update of pull request #${pullRequest.number}; `
        + `its head is ${pullRequest.head.sha}`,
      );
      return 'stale';
    }
    const updated = await github.rest.pulls.update({
      owner,
      repo,
      pull_number: pullRequest.number,
      title: TITLE,
      body,
    });
    pullRequest = updated.data;
    core.info(`Updated promotion pull request #${pullRequest.number}`);
  } else {
    try {
      const created = await github.rest.pulls.create({
        owner,
        repo,
        head: HEAD,
        base: BASE,
        title: TITLE,
        body,
        draft: false,
      });
      pullRequest = created.data;
      core.info(`Created promotion pull request #${pullRequest.number}`);
    } catch (error) {
      if (error.status !== 422) {
        throw error;
      }
      exactPulls = await listExactOpenPulls();
      if (exactPulls.length !== 1) {
        throw new Error(
          `Promotion pull request creation was rejected and exact-pair recovery failed: ${error.message}`,
        );
      }
      pullRequest = exactPulls[0];
      validateExactPull(pullRequest);
      core.warning(`Reusing concurrently created promotion pull request #${pullRequest.number}`);
      const updated = await github.rest.pulls.update({
        owner,
        repo,
        pull_number: pullRequest.number,
        title: TITLE,
        body,
      });
      pullRequest = updated.data;
    }
  }

  validateExactPull(pullRequest);
  if (!await stillCurrent('auto-merge enablement')) {
    return 'stale';
  }

  let detail = await github.rest.pulls.get({
    owner,
    repo,
    pull_number: pullRequest.number,
  });
  pullRequest = detail.data;
  validateExactPull(pullRequest);
  if (pullRequest.head.sha !== publishedSha) {
    core.notice(
      `Skipping stale auto-merge enablement for pull request #${pullRequest.number}; `
      + `its head is ${pullRequest.head.sha}`,
    );
    return 'stale';
  }

  if (pullRequest.auto_merge) {
    if (pullRequest.auto_merge.merge_method !== 'merge') {
      throw new Error(
        `Pull request #${pullRequest.number} already has auto-merge enabled with `
        + `${pullRequest.auto_merge.merge_method}, not merge`,
      );
    }
    core.info(`Auto-merge is already enabled for pull request #${pullRequest.number}`);
    return 'auto-merge-already-enabled';
  }

  try {
    await github.graphql(
      `mutation EnablePromotionAutoMerge($pullRequestId: ID!) {
        enablePullRequestAutoMerge(input: {
          pullRequestId: $pullRequestId,
          mergeMethod: MERGE
        }) {
          clientMutationId
        }
      }`,
      { pullRequestId: pullRequest.node_id },
    );
  } catch (error) {
    detail = await github.rest.pulls.get({
      owner,
      repo,
      pull_number: pullRequest.number,
    });
    pullRequest = detail.data;
    if (pullRequest.merged) {
      core.info(`Promotion pull request #${pullRequest.number} merged while enabling auto-merge`);
      return 'merged';
    }
    if (pullRequest.auto_merge?.merge_method === 'merge') {
      core.warning(
        `Auto-merge became enabled concurrently for pull request #${pullRequest.number}`,
      );
      return 'auto-merge-already-enabled';
    }
    throw error;
  }

  detail = await github.rest.pulls.get({
    owner,
    repo,
    pull_number: pullRequest.number,
  });
  pullRequest = detail.data;
  if (pullRequest.merged) {
    core.info(`Promotion pull request #${pullRequest.number} merged after auto-merge was enabled`);
    return 'merged';
  }
  if (pullRequest.auto_merge?.merge_method !== 'merge') {
    throw new Error(`Could not verify merge-commit auto-merge for pull request #${pullRequest.number}`);
  }

  core.info(`Enabled merge-commit auto-merge for pull request #${pullRequest.number}`);
  return 'auto-merge-enabled';
};
