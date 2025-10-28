# kcp Project Governance

The kcp project is dedicated to democratizing Control Planes beyond container
orchestration. This governance explains how the project is run.

- [Manifesto](#values)
- [Values](#values)
- [Maintainers](#maintainers)
- [Code of Conduct Enforcement](#code-of-conduct)
- [Security Response Team](#security-response-team)
- [Voting](#voting)
- [Modifying this Charter](modifying-this-charter)

## Manifesto

 * kcp maintainers strive to be good citizens in the Kubernetes project.
 * kcp maintainers see kcp always as part of the Kubernetes ecosystem and always
   strive to keep that ecosystem united. In particular, this means:
   * kcp strives to not divert from Kubernetes, but strives to extend its
     use-cases to non-container control planes while keeping the ecosystems of
     libraries and tooling united.
   * kcp – as a consumer of Kubernetes API Machinery – will strive to stay 100%
     compatible with the semantics of Kubernetes APIs, while removing container
     orchestration specific functionality.
   * kcp strives to upstream changes to Kubernetes code as much as possible.

## Values

The kcp and its leadership embrace the following values:

 * *Openness*: Communication and decision-making happens in the open and is
   discoverable for future reference. As much as possible, all discussions and
   work take place in public forums and open repositories.
 * *Fairness*: All stakeholders have the opportunity to provide feedback and
   submit contributions, which will be considered on their merits.
 * *Community over Product or Company*: Sustaining and growing our community
   takes priority over shipping code or sponsors' organizational goals. Each
   contributor participates in the project as an individual. To be explicit,
   this means that all maintainers pledge to act in a [vendor-neutral](https://contribute.cncf.io/maintainers/community/vendor-neutrality/)
   way while participating in kcp development.
 * *Inclusivity*: We innovate through different perspectives and skill sets,
   which can only be accomplished in a welcoming and respectful environment.
 * *Participation*: Responsibilities within the project are earned through
   participation, and there is a clear path up the contributor ladder into
   leadership positions.

## Maintainers

kcp maintainers have write access to the [project GitHub repository](https://github.com/kcp-dev/kcp).
They can merge their own patches or patches from others. The current maintainers
can be found as top-level approvers in [OWNERS](./OWNERS).  Maintainers collectively
manage the project's resources and contributors.

This privilege is granted with some expectation of responsibility: maintainers
are people who care about the kcp project and want to help it grow and
improve. A maintainer is not just someone who can make changes, but someone who
has demonstrated their ability to collaborate with the team, get the most
knowledgeable people to review code and docs, contribute high-quality code, and
follow through to fix issues (in code or tests).

A maintainer is a contributor to the project's success and a citizen helping
the project succeed.

The collective team of all Maintainers is known as the Maintainer Council, which
is the governing body for the project.

### Security Response Team

The Maintainers will appoint a Security Response Team to handle security reports.
This committee may simply consist of the Maintainer Council themselves. If this
responsibility is delegated, the Maintainers will appoint a team of at least two
contributors to handle it. The Maintainers will review who is assigned to this
at least once a year.

The Security Response Team is responsible for handling all reports of security
holes and breaches according to the [security policy](./SECURITY.md).

The members of the Security Response Team are documented in [MAINTAINERS.md](./MAINTAINERS.md).

### GitHub Admin Team

The maintainers will appoint a GitHub Admin Team to handle ownership of the GitHub organization(s) owned by the kcp project. Members of the GitHub Admin Team need to be extremely trustworthy individuals with a long-standing trusted relationship to the project.

The team's responsibility is being administrators for the GitHub organization(s). This would include managing organization-wide permissions, creating new repositories, configuring the organization, etc. The GitHub Admin Team is an executive organ of the full Maintainer Council with the goal to reduce broad permissions, but members of the team are bound by Maintainer Council decisions. Members of the GitHub Admin Team must not be from a single employer/organization.

The members of the GitHub Admin Team are documented in [MAINTAINERS.md](./MAINTAINERS.md).

## Becoming a Maintainer

<!-- If you have full Contributor Ladder documentation that covers becoming
a Maintainer or Owner, then this section should instead be a reference to that
documentation -->

To become a [Maintainer](./MAINTAINERS.md) you need to demonstrate the following:

  * commitment to the project:
    * participate in discussions, contributions, code and documentation reviews
      for 3 months or more,
    * perform reviews for 5 non-trivial pull requests,
    * contribute 5 non-trivial pull requests and have them merged,
  * ability to write quality code and/or documentation,
  * ability to collaborate with the team,
  * understanding of how the team works (policies, processes for testing and code review, etc),
  * understanding of the project's code base and coding and documentation style.
  <!-- add any additional Maintainer requirements here -->

A new Maintainer must be proposed by an existing maintainer by sending a message to the
[developer mailing list](https://groups.google.com/g/kcp-dev). A simple majority
vote of existing Maintainers approves the application.

Maintainers who are selected will be granted the necessary GitHub rights,
and invited to the [private maintainer mailing list](https://groups.google.com/g/kcp-dev-private).

### Bootstrapping Maintainers

To bootstrap the process, 3 maintainers are defined (in the initial PR adding
this to the repository) that do not necessarily follow the above rules. When a
new maintainer is added following the above rules, the existing maintainers
define one not following the rules to step down, until all of them follow the
rules.

### Removing a Maintainer

Maintainers may resign at any time if they feel that they will not be able to
continue fulfilling their project duties.

Maintainers may also be removed after being inactive, failure to fulfill their
Maintainer responsibilities, violating the Code of Conduct, or other reasons.
Inactivity is defined as a period of very low or no activity in the project for
a year or more, with no definite schedule to return to full Maintainer activity.

A Maintainer may be removed at any time by a 2/3 vote of the remaining maintainers.

Depending on the reason for removal, a Maintainer may be converted to Emeritus
status. Emeritus Maintainers will still be consulted on some project matters,
and can be rapidly returned to Maintainer status if their availability changes.


## Meetings

Time zones permitting, Maintainers are expected to participate in the public
community call meeting. Maintainers will also have closed meetings in order to
discuss security reports. Such meetings should be scheduled by any Maintainer on
receipt of a security issue. All current Maintainers must be invited to such closed meetings.

## Code of Conduct

<!-- This assumes that your project does not have a separate Code of Conduct
Committee; most maintainer-run projects do not.  Remember to place a link
to the private Maintainer mailing list or alias in the code-of-conduct file.-->

kcp has adopted the CNCF [Code of Conduct](./code-of-conduct.md). Reporting of
Code of Conduct violations happen through the [CNCF Code of Conduct committee](./code-of-conduct.md#reporting)
and kcp maintainers pledge to work with the committee to resolve any incidents
occurring in the kcp community.


## Voting

While most business in kcp is conducted by "lazy consensus", periodically
the Maintainers may need to vote on specific actions or changes.
A vote can be taken on [the developer mailing list](https://groups.google.com/g/kcp-dev) or
[the private Maintainer mailing list](https://groups.google.com/g/kcp-dev-private)
for security issues.  Votes may also be taken at the community call
meeting. Any Maintainer may demand a vote be taken.

Most votes require a simple majority of all Maintainers to succeed. Maintainers
can be removed by a 2/3 majority vote of all Maintainers, and changes to this
Governance require a 2/3 vote of all Maintainers.

Pull requests that make changes requiring Maintainer consensus may also be
understood as a vote. They require the stated majority to be granted via
LGTMs on the pull request. Such a pull request shall be announced to the developer
mailing list and put on hold until the necessary majority has been reached.

## Subprojects

Any Maintainer may submit a [vote](#voting) to create a new subproject under the
kcp-dev GitHub organization. Subprojects are governed by all Maintainers, but may
take on additional Subproject Maintainers that are only responsible for the
specific subproject.

It is the combined responsibility of Maintainers and Subproject Maintainers
to review contributions to subprojects and make project goal decisions.
Subproject Maintainers are not part of the private Maintainer mailing list and
are involved in security responses on a need-to-know basis if the reported security
issue concerns their respective subproject.

Subproject Maintainers are elected by the Maintainers. Subproject Maintainers are
allowed to participate in votes concerning their respective subprojects.

## Approvers

The Maintainers and Subproject Maintainers may elect trusted contributors to
assist them in the review process for specific parts of the code. Those Approvers
are allowed to approve and merge code contributions for certain subsets of the code
(not a whole project), e.g. areas of the code that they have proven themselves
to be very familiar with.

Approvers do not have voting rights.

## Modifying this Charter

Changes to this Governance and its supporting documents may be approved by a
2/3 vote of the Maintainers.
