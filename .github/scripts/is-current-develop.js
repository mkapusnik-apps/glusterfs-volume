'use strict';

module.exports = async ({ github, context, core }) => {
  if (context.ref !== 'refs/heads/develop') {
    throw new Error(`Ref ${context.ref} is not the develop branch`);
  }

  let branch;
  try {
    branch = await github.rest.repos.getBranch({
      owner: context.repo.owner,
      repo: context.repo.repo,
      branch: 'develop',
    });
  } catch (error) {
    const status = error.status ? `${error.status} ` : '';
    throw new Error(`Failed to verify the develop branch head: ${status}${error.message}`);
  }

  if (branch.data.commit.sha !== context.sha) {
    const purpose = process.env.CHECK_PURPOSE || 'mutable operation';
    core.notice(
      `Skipping stale ${purpose} for ${context.sha}; develop is now ${branch.data.commit.sha}`,
    );
    return 'false';
  }

  return 'true';
};
