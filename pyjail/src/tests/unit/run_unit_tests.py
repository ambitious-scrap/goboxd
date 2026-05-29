"""Script to run all unit tests"""

import sys

from tests.unit.test_classes import NsJailRunCommandTest
from utils import logger


class UnitTestRunner:
    """Class to run all unit tests in the test bank"""

    def __init__(self):
        self.tests = []
        self.failed = []
        # Add test for NsJailRunCommandTest

        self.tests.append(
            NsJailRunCommandTest(
                args=["/bin/echo", "Hello"],
                expected_out="Hello\n",
                expected_err="",
            ),
        )

    def run_tests(self):
        """Runs all tests"""
        for test in self.tests:
            test.run()

    def validate_results(self):
        """Validates all results"""
        result = True
        for test in self.tests:
            if not test.validate():
                result = False
                self.failed.append(test)
        return result

    def run_test_suite(self):
        """
        Runs tests, validates them, and returns True or False depending on
        whether the tests passed or not
        """
        self.run_tests()
        return self.validate_results()


def main(verbose=False):
    """Runs all tests"""
    unit_test_runner = UnitTestRunner()
    if not unit_test_runner.run_test_suite():
        if verbose:
            for test in unit_test_runner.failed:
                logger.info("Failed test")
                logger.info(
                    f"Args {test.args}\nexpected out: {test.expected_out}\nActual out: {test.out}",
                )
        return 2
    logger.info("Passed all tests")
    return 0


if __name__ == "__main__":
    VERBOSE = True
    sys.exit(main(VERBOSE))
